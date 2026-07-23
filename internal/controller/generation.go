package controller

import (
	"context"
	"fmt"
	"strings"
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
	if !gen.DeletionTimestamp.IsZero() {
		return r.cleanup(ctx, &gen, start)
	}

	switch gen.Status.Phase {
	case "":
		return r.createJob(ctx, &gen, start)
	case breakfixv1.GenerationRunning:
		return r.trackJob(ctx, &gen, start)
	case breakfixv1.GenerationVerifying:
		return r.trackVerification(ctx, &gen, start)
	case breakfixv1.GenerationVerified, breakfixv1.GenerationFailed:
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

	if gen.Spec.EnvSecretRef != generationEnvSecretName(gen.Name) {
		return r.failGeneration(ctx, gen, "generation env secret reference is invalid")
	}
	jobName := "gen-" + k8s.RandomID()

	if err := r.K8s.CreateJob(r.CRDNamespace, jobName, k8s.CreateJobOpts{
		Image:           gen.Spec.Image,
		EnvSecretName:   gen.Spec.EnvSecretRef,
		ImagePullPolicy: corev1.PullAlways,
	}); err != nil {
		gen.Status.Phase = breakfixv1.GenerationFailed
		gen.Status.Message = fmt.Sprintf("create job: %v", err)
		now := metav1.Now()
		gen.Status.CompletedAt = &now
		r.Status().Update(ctx, gen) //nolint:errcheck
		return ctrl.Result{}, err
	}

	gen.Status.Phase = breakfixv1.GenerationRunning
	gen.Status.JobName = jobName
	gen.Status.VerifyTaskRef = ""
	gen.Status.Message = "generating challenge artifact"
	gen.Status.Attempt++
	now := metav1.Now()
	if gen.Status.StartedAt == nil {
		gen.Status.StartedAt = &now
	}
	gen.Status.CompletedAt = nil
	slog.Info("job created", "generation", gen.Name, "job", jobName, "duration", time.Since(phaseStart))

	if err := r.Status().Update(ctx, gen); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("reconcile done", "resource", "generation", "name", gen.Name, "phase", gen.Status.Phase, "duration", time.Since(reconcileStart))
	return ctrl.Result{}, nil
}

func generationEnvSecretName(generationName string) string {
	return generationName + "-env"
}

func (r *GenerationReconciler) failGeneration(ctx context.Context, gen *breakfixv1.Generation, message string) (ctrl.Result, error) {
	gen.Status.Phase = breakfixv1.GenerationFailed
	gen.Status.Message = message
	now := metav1.Now()
	gen.Status.CompletedAt = &now
	if err := r.Status().Update(ctx, gen); err != nil {
		return ctrl.Result{}, err
	}
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
			// The generator reports verification scheduling through Gateway before it
			// exits. Read the uncached CRD so a stale controller event cannot
			// overwrite that state with the old Running value.
			fresh, freshErr := r.K8s.GetGeneration(ctx, r.CRDNamespace, gen.Name)
			if freshErr == nil && fresh.Status.Phase == breakfixv1.GenerationVerifying {
				return ctrl.Result{}, nil
			}
			if gen.Status.Message != "generator finished, waiting for artifact upload and verification scheduling" {
				gen.Status.Message = "generator finished, waiting for artifact upload and verification scheduling"
				r.Status().Update(ctx, gen) //nolint:errcheck
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			return r.failGeneration(ctx, gen, "generator finished without uploading an artifact")
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

func (r *GenerationReconciler) trackVerification(ctx context.Context, gen *breakfixv1.Generation, reconcileStart time.Time) (ctrl.Result, error) {
	if strings.TrimSpace(gen.Status.VerifyTaskRef) == "" {
		return r.failGeneration(ctx, gen, "verification phase has no VerifyTask reference")
	}
	task, err := r.K8s.GetVerifyTask(ctx, r.CRDNamespace, gen.Status.VerifyTaskRef)
	if err != nil {
		gen.Status.Message = fmt.Sprintf("waiting for verify task %s", gen.Status.VerifyTaskRef)
		if updateErr := r.Status().Update(ctx, gen); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	switch task.Status.Phase {
	case breakfixv1.VerifyTaskSucceeded:
		gen.Status.Phase = breakfixv1.GenerationVerified
		gen.Status.Message = "artifact passed real verification"
		now := metav1.Now()
		gen.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, gen); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	case breakfixv1.VerifyTaskFailed:
		gen.Status.Phase = breakfixv1.GenerationFailed
		gen.Status.Message = generationFailureMessage(task)
		now := metav1.Now()
		gen.Status.CompletedAt = &now
		if err := r.Status().Update(ctx, gen); err != nil {
			return ctrl.Result{}, err
		}
		slog.Info("verification failed", "generation", gen.Name, "duration", time.Since(reconcileStart))
		return ctrl.Result{}, nil
	default:
		gen.Status.Message = "artifact verification running"
	}
	if err := r.Status().Update(ctx, gen); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *GenerationReconciler) cleanup(ctx context.Context, gen *breakfixv1.Generation, reconcileStart time.Time) (ctrl.Result, error) {
	start := time.Now()
	if gen.Status.JobName != "" {
		if err := r.K8s.DeleteJob(r.CRDNamespace, gen.Status.JobName); err != nil {
			return ctrl.Result{}, fmt.Errorf("delete generation job: %w", err)
		}
	}
	if gen.Spec.EnvSecretRef == generationEnvSecretName(gen.Name) {
		if err := r.K8s.DeleteSecret(r.CRDNamespace, gen.Spec.EnvSecretRef); err != nil {
			return ctrl.Result{}, fmt.Errorf("delete generation secret: %w", err)
		}
	}
	controllerutil.RemoveFinalizer(gen, generationFinalizer)
	if err := r.Update(ctx, gen); err != nil {
		return ctrl.Result{}, err
	}
	slog.Info("reconcile done", "resource", "generation", "name", gen.Name, "action", "cleanup", "duration", time.Since(start))
	return ctrl.Result{}, nil
}

func generationFailureMessage(task *breakfixv1.VerifyTask) string {
	if task == nil {
		return "verification failed"
	}
	base := strings.TrimSpace(task.Status.Message)
	if task.Status.Report == nil {
		return base
	}
	if summary := strings.TrimSpace(task.Status.Report.Summary); summary != "" {
		if base == "" || base == summary {
			return summary
		}
		return base + ": " + summary
	}
	for _, issue := range task.Status.Report.Issues {
		if message := strings.TrimSpace(issue.Message); message != "" {
			return message
		}
	}
	return base
}

func (r *GenerationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&breakfixv1.Generation{}).
		Complete(r)
}
