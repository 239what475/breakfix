package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const generationFinalizer = "breakfix.dev/generation-cleanup"

type GenerationReconciler struct {
	client.Client
	K8s          *k8s.Client
	CRDNamespace string
}

func (r *GenerationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()

	var gen breakfixv1.Generation
	if err := r.Get(ctx, req.NamespacedName, &gen); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch gen.Status.Phase {
	case "":
		return r.createJob(ctx, &gen, start)
	case breakfixv1.GenerationRunning:
		return r.trackJob(ctx, &gen, start)
	case breakfixv1.GenerationSucceeded, breakfixv1.GenerationFailed:
		return r.cleanup(ctx, &gen, start)
	}

	return ctrl.Result{}, nil
}

func (r *GenerationReconciler) createJob(ctx context.Context, gen *breakfixv1.Generation, reconcileStart time.Time) (ctrl.Result, error) {
	phaseStart := time.Now()

	controllerutil.AddFinalizer(gen, generationFinalizer)
	if err := r.Update(ctx, gen); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.K8s.EnsureNamespace(r.CRDNamespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
	}

	jobName := "gen-" + k8s.RandomID()
	env := gen.Spec.Env
	if env == nil {
		env = map[string]string{}
	}

	if err := r.K8s.CreateJob(r.CRDNamespace, jobName, gen.Spec.Image, env); err != nil {
		gen.Status.Phase = breakfixv1.GenerationFailed
		gen.Status.Message = fmt.Sprintf("create job: %v", err)
		now := metav1.Now()
		gen.Status.CompletedAt = &now
		r.Status().Update(ctx, gen) //nolint:errcheck
		return ctrl.Result{}, err
	}

	gen.Status.Phase = breakfixv1.GenerationRunning
	gen.Status.JobName = jobName
	gen.Status.Message = "building and verifying challenge"
	now := metav1.Now()
	gen.Status.StartedAt = &now
	slog.Info("job created", "generation", gen.Name, "job", jobName, "duration", time.Since(phaseStart))

	if err := r.Status().Update(ctx, gen); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("reconcile done", "resource", "generation", "name", gen.Name, "phase", gen.Status.Phase, "duration", time.Since(reconcileStart))
	return ctrl.Result{}, nil
}

func (r *GenerationReconciler) trackJob(ctx context.Context, gen *breakfixv1.Generation, reconcileStart time.Time) (ctrl.Result, error) {
	phaseStart := time.Now()

	job, err := r.K8s.Clientset().BatchV1().Jobs(r.CRDNamespace).Get(ctx, gen.Status.JobName, metav1.GetOptions{})
	if err != nil {
		gen.Status.Phase = breakfixv1.GenerationFailed
		gen.Status.Message = fmt.Sprintf("job not found: %v", err)
		now := metav1.Now()
		gen.Status.CompletedAt = &now
		r.Status().Update(ctx, gen) //nolint:errcheck
		slog.Info("reconcile done", "resource", "generation", "name", gen.Name, "phase", gen.Status.Phase, "duration", time.Since(reconcileStart))
		return ctrl.Result{}, nil
	}

	for _, c := range job.Status.Conditions {
		switch c.Type {
		case batchv1.JobComplete:
			gen.Status.PodName = r.findJobPod(ctx, gen.Status.JobName)
			r.extractAndSetMetadata(ctx, gen)
			gen.Status.Phase = breakfixv1.GenerationSucceeded
			if gen.Status.Challenge != nil {
				gen.Status.Message = fmt.Sprintf("challenge %s generated", gen.Status.Challenge.ID)
			} else {
				gen.Status.Message = "challenge generated"
			}
			now := metav1.Now()
			gen.Status.CompletedAt = &now
			slog.Info("job done", "generation", gen.Name, "result", "success", "duration", time.Since(phaseStart))

		case batchv1.JobFailed:
			gen.Status.Phase = breakfixv1.GenerationFailed
			gen.Status.Message = fmt.Sprintf("job failed: %s", c.Message)
			now := metav1.Now()
			gen.Status.CompletedAt = &now
			slog.Error("job done", "generation", gen.Name, "result", "failed", "reason", c.Reason, "message", c.Message, "duration", time.Since(phaseStart))
		}
	}

	if gen.Status.Phase != breakfixv1.GenerationRunning {
		r.Status().Update(ctx, gen) //nolint:errcheck
		slog.Info("reconcile done", "resource", "generation", "name", gen.Name, "phase", gen.Status.Phase, "duration", time.Since(reconcileStart))
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *GenerationReconciler) findJobPod(ctx context.Context, jobName string) string {
	var pods corev1.PodList
	r.List(ctx, &pods, client.InNamespace(r.CRDNamespace), client.MatchingLabels{"job-name": jobName}) //nolint:errcheck
	if len(pods.Items) > 0 {
		return pods.Items[0].Name
	}
	return ""
}

func (r *GenerationReconciler) extractAndSetMetadata(ctx context.Context, gen *breakfixv1.Generation) {
	if gen.Status.PodName == "" {
		return
	}
	exitCode, out, err := r.K8s.ExecInPod(r.CRDNamespace, gen.Status.PodName, "cat", k8s.PodMetadataPath)
	if err != nil || exitCode != 0 {
		slog.Error("extract metadata failed", "err", err, "exit", exitCode)
		return
	}
	var meta breakfixv1.ChallengeSpec
	if err := json.Unmarshal([]byte(out), &meta); err != nil {
		slog.Error("parse metadata failed", "err", err)
		return
	}
	gen.Status.Challenge = &meta
}

func (r *GenerationReconciler) cleanup(ctx context.Context, gen *breakfixv1.Generation, reconcileStart time.Time) (ctrl.Result, error) {
	start := time.Now()
	controllerutil.RemoveFinalizer(gen, generationFinalizer)
	if err := r.Update(ctx, gen); err != nil {
		return ctrl.Result{}, err
	}
	slog.Info("reconcile done", "resource", "generation", "name", gen.Name, "action", "cleanup", "duration", time.Since(start))
	return ctrl.Result{}, nil
}

func (r *GenerationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&breakfixv1.Generation{}).
		Complete(r)
}
