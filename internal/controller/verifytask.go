package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifyTaskReconciler owns only real artifact verification. Publishing is a
// separate, explicit Gateway operation over a successful immutable task.
type VerifyTaskReconciler struct {
	client.Client
	K8s              *k8s.Client
	RegistryAddr     string
	RegistryInsecure bool
	CRDNamespace     string
	InternalAPIKey   string
	ServerHost       string
	ServerPort       int
}

func (r *VerifyTaskReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var task breakfixv1.VerifyTask
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch task.Status.Phase {
	case "", breakfixv1.VerifyTaskPending:
		if err := validateVerifyTaskSpec(&task); err != nil {
			return r.failTask(ctx, &task, "INVALID_VERIFY_TASK", err.Error())
		}
		return r.createVerifyJob(ctx, &task)
	case breakfixv1.VerifyTaskRunning:
		return r.trackVerifyJob(ctx, &task)
	case breakfixv1.VerifyTaskSucceeded, breakfixv1.VerifyTaskFailed:
		return ctrl.Result{}, nil
	default:
		return ctrl.Result{}, nil
	}
}

func validateVerifyTaskSpec(task *breakfixv1.VerifyTask) error {
	if task == nil {
		return fmt.Errorf("verify task is nil")
	}
	if !challenge.ValidID(task.Spec.ChallengeID) {
		return fmt.Errorf("invalid challenge id %q", task.Spec.ChallengeID)
	}
	if strings.TrimSpace(task.Spec.Submission.ID) == "" {
		return fmt.Errorf("submission id is required")
	}
	if task.Spec.Source.Kind != "agent" {
		return fmt.Errorf("invalid verify task source kind %q", task.Spec.Source.Kind)
	}
	if strings.TrimSpace(task.Spec.Source.Ref) == "" {
		return fmt.Errorf("agent source requires a generation reference")
	}
	return nil
}

func (r *VerifyTaskReconciler) failTask(ctx context.Context, task *breakfixv1.VerifyTask, code, message string) (ctrl.Result, error) {
	task.Status.Phase = breakfixv1.VerifyTaskFailed
	task.Status.Message = strings.TrimSpace(message)
	now := metav1.Now()
	task.Status.CompletedAt = &now
	task.Status.Report = &breakfixv1.VerifyReport{
		Summary: task.Status.Message,
		Issues: []breakfixv1.VerifyIssue{{
			Code:    code,
			Message: task.Status.Message,
		}},
	}
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VerifyTaskReconciler) createVerifyJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	jobName := "verify-" + k8s.RandomID()
	env := map[string]string{
		"BREAKFIX_MODE":            "verify",
		"VERIFY_TASK_ID":           task.Name,
		"VERIFY_TASK_NAMESPACE":    r.CRDNamespace,
		"VERIFY_SUBMISSION_ID":     task.Spec.Submission.ID,
		"GATEWAY_INTERNAL_URL":     r.internalGatewayURL(),
		"GATEWAY_INTERNAL_API_KEY": r.InternalAPIKey,
		"REGISTRY_ADDR":            r.RegistryAddr,
		"LAB_NAMESPACE":            r.CRDNamespace,
	}
	if r.RegistryInsecure {
		env["REGISTRY_INSECURE"] = "true"
	}

	if err := r.K8s.CreateJob(r.CRDNamespace, jobName, k8s.CreateJobOpts{
		Image:           generatorImage(r.RegistryAddr),
		Env:             env,
		ImagePullPolicy: corev1.PullAlways,
	}); err != nil {
		return r.failTask(ctx, task, "VERIFY_JOB_CREATE_FAILED", fmt.Sprintf("create verify job: %v", err))
	}

	task.Status.Phase = breakfixv1.VerifyTaskRunning
	task.Status.JobName = jobName
	task.Status.Message = "verify job created"
	now := metav1.Now()
	task.Status.StartedAt = &now
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *VerifyTaskReconciler) trackVerifyJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	job, err := r.K8s.Clientset().BatchV1().Jobs(r.CRDNamespace).Get(ctx, task.Status.JobName, metav1.GetOptions{})
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	for _, condition := range job.Status.Conditions {
		switch condition.Type {
		case batchv1.JobComplete:
			refreshed, err := r.K8s.GetVerifyTask(ctx, r.CRDNamespace, task.Name)
			if err != nil {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}
			if refreshed.Status.Phase == breakfixv1.VerifyTaskRunning {
				return r.failTask(ctx, refreshed, "VERIFY_RESULT_MISSING", "verify job finished without reporting result")
			}
			return ctrl.Result{}, nil
		case batchv1.JobFailed:
			return r.failTask(ctx, task, "VERIFY_JOB_FAILED", fmt.Sprintf("verify job failed: %s", condition.Message))
		}
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *VerifyTaskReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&breakfixv1.VerifyTask{}).
		Complete(r)
}

func (r *VerifyTaskReconciler) internalGatewayURL() string {
	host := strings.TrimSpace(r.ServerHost)
	if host == "" {
		host = "172.18.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, r.ServerPort)
}

func generatorImage(registryAddr string) string {
	if strings.TrimSpace(registryAddr) == "" {
		return "breakfix-generator:latest"
	}
	return registryAddr + "/breakfix-generator:latest"
}
