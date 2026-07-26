package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/verification"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const verifyTaskFinalizer = "breakfix.dev/verify-task-cleanup"

// VerifyTaskReconciler owns only real artifact verification. Publishing is a
// separate, explicit Server operation over a successful immutable task.
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
	if !task.DeletionTimestamp.IsZero() {
		return r.cleanupTask(ctx, &task, true)
	}
	if !controllerutil.ContainsFinalizer(&task, verifyTaskFinalizer) {
		controllerutil.AddFinalizer(&task, verifyTaskFinalizer)
		if err := r.Update(ctx, &task); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
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
		return r.cleanupTask(ctx, &task, false)
	default:
		return ctrl.Result{}, nil
	}
}

func validateVerifyTaskSpec(task *breakfixv1.VerifyTask) error {
	if task == nil {
		return fmt.Errorf("verify task is nil")
	}
	if !validVerifyTaskChallengeID(task.Spec.ChallengeID) {
		return fmt.Errorf("invalid challenge id %q", task.Spec.ChallengeID)
	}
	if strings.TrimSpace(task.Spec.Submission.ID) == "" {
		return fmt.Errorf("submission id is required")
	}
	if strings.TrimSpace(task.Spec.Source.Ref) == "" {
		return fmt.Errorf("source reference is required")
	}
	return nil
}

func validVerifyTaskChallengeID(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '-' && i > 0 && i < len(id)-1) {
			continue
		}
		return false
	}
	return true
}

func (r *VerifyTaskReconciler) failTask(ctx context.Context, task *breakfixv1.VerifyTask, code, message string) (ctrl.Result, error) {
	if task == nil {
		return ctrl.Result{}, nil
	}
	current, err := r.K8s.GetVerifyTask(ctx, r.CRDNamespace, task.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if current.Status.Phase == breakfixv1.VerifyTaskSucceeded || current.Status.Phase == breakfixv1.VerifyTaskFailed {
		return ctrl.Result{}, nil
	}
	current.Status.Phase = breakfixv1.VerifyTaskFailed
	current.Status.Message = strings.TrimSpace(message)
	now := metav1.Now()
	current.Status.CompletedAt = &now
	current.Status.Report = &breakfixv1.VerifyReport{
		Class:   breakfixv1.VerifyFailureInfrastructure,
		Summary: current.Status.Message,
		Issues: []breakfixv1.VerifyIssue{{
			Code:    code,
			Message: current.Status.Message,
		}},
	}
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, current); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VerifyTaskReconciler) createVerifyJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	jobName := verification.JobName(task.Name)
	env := map[string]string{
		"VERIFY_TASK_ID":          task.Name,
		"VERIFY_TASK_NAMESPACE":   r.CRDNamespace,
		"VERIFY_SUBMISSION_ID":    task.Spec.Submission.ID,
		"SERVER_INTERNAL_URL":     r.internalServerURL(),
		"SERVER_INTERNAL_API_KEY": r.InternalAPIKey,
		"REGISTRY_ADDR":           r.RegistryAddr,
	}
	if r.RegistryInsecure {
		env["REGISTRY_INSECURE"] = "true"
	}

	// The verifier currently uses rootful BuildKit. Its ServiceAccount remains
	// independent and narrowly scoped; moving this to rootless BuildKit needs a
	// separate real Kubernetes POC before the privilege can be removed.
	privileged := true
	backoffLimit := int32(2)
	if _, _, err := r.K8s.CreateOrGetJob(r.CRDNamespace, jobName, k8s.CreateJobOpts{
		Image:              verifierImage(r.RegistryAddr),
		ContainerName:      "verifier",
		ServiceAccountName: "breakfix-verifier",
		Privileged:         &privileged,
		BackoffLimit:       &backoffLimit,
		Env:                env,
		ImagePullPolicy:    corev1.PullAlways,
		Labels: map[string]string{
			"breakfix.dev/verify-task": task.Name,
		},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(task, breakfixv1.SchemeGroupVersion.WithKind("VerifyTask"))},
	}); err != nil {
		return r.failTask(ctx, task, "VERIFY_JOB_CREATE_FAILED", fmt.Sprintf("create verify job: %v", err))
	}

	task.Status.Phase = breakfixv1.VerifyTaskRunning
	task.Status.JobName = jobName
	task.Status.TempImage = verification.ImageName(r.RegistryAddr, task.Name)
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

func (r *VerifyTaskReconciler) cleanupTask(ctx context.Context, task *breakfixv1.VerifyTask, deleting bool) (ctrl.Result, error) {
	if task == nil {
		return ctrl.Result{}, nil
	}
	selector := "breakfix.dev/verify-task=" + task.Name
	containerEnvironments, err := r.K8s.ListContainerEnvironments(ctx, r.CRDNamespace, selector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list verification container environments: %w", err)
	}
	vclusterEnvironments, err := r.K8s.ListVClusterEnvironments(ctx, r.CRDNamespace, selector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("list verification vcluster environments: %w", err)
	}
	for _, env := range containerEnvironments.Items {
		if err := r.K8s.DeleteContainerEnvironment(ctx, r.CRDNamespace, env.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("delete verification container environment %s: %w", env.Name, err)
		}
	}
	for _, env := range vclusterEnvironments.Items {
		if err := r.K8s.DeleteVClusterEnvironment(ctx, r.CRDNamespace, env.Name); err != nil {
			return ctrl.Result{}, fmt.Errorf("delete verification vcluster environment %s: %w", env.Name, err)
		}
	}
	if len(containerEnvironments.Items) != 0 || len(vclusterEnvironments.Items) != 0 {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if task.Status.Phase == breakfixv1.VerifyTaskFailed && strings.TrimSpace(task.Status.TempImage) != "" {
		if err := registry.DeleteImage(ctx, task.Status.TempImage, r.RegistryInsecure); err != nil {
			return ctrl.Result{}, fmt.Errorf("delete failed verification image: %w", err)
		}
	}
	if deleting {
		controllerutil.RemoveFinalizer(task, verifyTaskFinalizer)
		if err := r.Update(ctx, task); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *VerifyTaskReconciler) internalServerURL() string {
	host := strings.TrimSpace(r.ServerHost)
	if host == "" {
		host = "172.18.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, r.ServerPort)
}

func verifierImage(registryAddr string) string {
	if strings.TrimSpace(registryAddr) == "" {
		return "breakfix-verifier:latest"
	}
	return registryAddr + "/breakfix-verifier:latest"
}
