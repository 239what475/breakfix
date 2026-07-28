package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/verification"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const verifyTaskFinalizer = "breakfix.dev/verify-task-cleanup"

// VerifyTaskReconciler owns the durable verification workflow. Its Build,
// Publisher, and Verifier are ordinary, deterministic Kubernetes Jobs with
// distinct trust boundaries; VerifyTask remains the only business CRD.
type VerifyTaskReconciler struct {
	client.Client
	K8s                  *k8s.Client
	RegistryAddr         string
	RegistryInsecure     bool
	RegistryUsername     string
	RegistryPassword     string
	RegistryPullSecret   string
	RegistryWriteSecret  string
	BuilderImage         string
	PublisherImage       string
	VerifierImage        string
	CRDNamespace         string
	VerificationGrantKey []byte
	ServerHost           string
	ServerPort           int
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
			return r.failTask(ctx, &task, breakfixv1.VerifyFailureInfrastructure, "INVALID_VERIFY_TASK", err.Error())
		}
		return r.createBuildJob(ctx, &task)
	case breakfixv1.VerifyTaskRunning:
		return r.reconcileRunningTask(ctx, &task)
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
	if strings.TrimSpace(task.Spec.Submission.ID) == "" {
		return fmt.Errorf("submission id is required")
	}
	if strings.TrimSpace(task.Spec.Source.Ref) == "" {
		return fmt.Errorf("source reference is required")
	}
	runtime := challenge.NormalizeRuntime(task.Spec.Execution.Runtime)
	if runtime != challenge.RuntimeContainer && runtime != challenge.RuntimeVCluster {
		return fmt.Errorf("execution runtime must be container or vcluster")
	}
	if len(task.Spec.Execution.CheckpointIDs) == 0 {
		return fmt.Errorf("execution checkpoint IDs are required")
	}
	seen := make(map[string]struct{}, len(task.Spec.Execution.CheckpointIDs))
	for _, id := range task.Spec.Execution.CheckpointIDs {
		if !challenge.ValidID(id) {
			return fmt.Errorf("invalid execution checkpoint ID %q", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate execution checkpoint ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func (r *VerifyTaskReconciler) reconcileRunningTask(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	switch task.Status.Stage {
	case breakfixv1.VerifyTaskBuilding:
		return r.trackBuildJob(ctx, task)
	case breakfixv1.VerifyTaskPublishing:
		return r.trackPublisherJob(ctx, task)
	case breakfixv1.VerifyTaskVerifying:
		return r.trackVerifierJob(ctx, task)
	default:
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "VERIFY_STAGE_MISSING", "running verify task has no valid stage")
	}
}

func (r *VerifyTaskReconciler) createBuildJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	submissionGrant, err := r.issueGrant(task, verification.GrantSubmissionDownload, "")
	if err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "BUILD_GRANT_ISSUE_FAILED", err.Error())
	}
	baseGrant, err := r.issueGrant(task, verification.GrantBaseDownload, task.Spec.Execution.Runtime)
	if err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "BUILD_GRANT_ISSUE_FAILED", err.Error())
	}
	uploadGrant, err := r.issueGrant(task, verification.GrantOCIUpload, "")
	if err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "BUILD_GRANT_ISSUE_FAILED", err.Error())
	}
	jobName := verification.BuildJobName(task.Name)
	backoff := int32(0)
	nonPrivileged := false
	automount := false
	if _, _, err := r.K8s.CreateOrGetJob(r.CRDNamespace, jobName, k8s.CreateJobOpts{
		Image:                        r.BuilderImage,
		ContainerName:                "builder",
		Privileged:                   &nonPrivileged,
		AutomountServiceAccountToken: &automount,
		BackoffLimit:                 &backoff,
		Env: map[string]string{
			"VERIFY_SERVER_URL":       r.internalServerURL(),
			"VERIFY_TASK_ID":          task.Name,
			"VERIFY_SUBMISSION_GRANT": submissionGrant,
			"VERIFY_BASE_GRANT":       baseGrant,
			"VERIFY_UPLOAD_GRANT":     uploadGrant,
			"VERIFY_BASE_NAME":        builderBaseName(task.Spec.Execution.Runtime),
		},
		PodSecurityContext: buildPodSecurityContext(),
		SecurityContext:    buildSecurityContext(),
		Volumes:            []corev1.Volume{buildScratchVolume()},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "buildkitd",
			MountPath: "/home/user/.local/share/buildkit",
		}},
		Resources:       buildResources(),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Labels: map[string]string{
			"breakfix.dev/verify-task": task.Name,
			"breakfix.dev/role":        "build",
		},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(task, breakfixv1.SchemeGroupVersion.WithKind("VerifyTask"))},
	}); err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "BUILD_JOB_CREATE_FAILED", fmt.Sprintf("create build job: %v", err))
	}
	task.Status.Phase = breakfixv1.VerifyTaskRunning
	task.Status.Stage = breakfixv1.VerifyTaskBuilding
	task.Status.BuildJobName = jobName
	task.Status.StagingImage = verification.ImageName(r.RegistryAddr, task.Name)
	task.Status.Message = "untrusted build job created"
	now := metav1.Now()
	task.Status.StartedAt = &now
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *VerifyTaskReconciler) trackBuildJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	finished, succeeded, exitCode, message, err := r.jobTerminalState(ctx, task.Status.BuildJobName)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if !finished {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if !succeeded {
		class := breakfixv1.VerifyFailureInfrastructure
		if exitCode == 20 {
			class = breakfixv1.VerifyFailureArtifact
		}
		return r.failTask(ctx, task, class, "BUILD_JOB_FAILED", fmt.Sprintf("build job failed (exit=%d): %s", exitCode, message))
	}
	return r.createPublisherJob(ctx, task)
}

func (r *VerifyTaskReconciler) createPublisherJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	downloadGrant, err := r.issueGrant(task, verification.GrantOCIDownload, "")
	if err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "PUBLISH_GRANT_ISSUE_FAILED", err.Error())
	}
	jobName := verification.PublisherJobName(task.Name)
	backoff := int32(0)
	nonPrivileged := false
	automount := false
	env := map[string]string{
		"VERIFY_SERVER_URL":    r.internalServerURL(),
		"VERIFY_TASK_ID":       task.Name,
		"VERIFY_ARCHIVE_GRANT": downloadGrant,
		"VERIFY_STAGING_IMAGE": task.Status.StagingImage,
	}
	if r.RegistryInsecure {
		env["REGISTRY_INSECURE"] = "true"
	}
	if _, _, err := r.K8s.CreateOrGetJob(r.CRDNamespace, jobName, k8s.CreateJobOpts{
		Image:                        r.PublisherImage,
		ContainerName:                "publisher",
		Privileged:                   &nonPrivileged,
		AutomountServiceAccountToken: &automount,
		BackoffLimit:                 &backoff,
		Env:                          env,
		EnvSecretNames:               []string{r.RegistryWriteSecret},
		SecurityContext:              trustedJobSecurityContext(),
		Resources:                    publisherResources(),
		Volumes:                      []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		VolumeMounts:                 []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		ImagePullPolicy:              corev1.PullIfNotPresent,
		ImagePullSecrets:             []corev1.LocalObjectReference{{Name: r.RegistryPullSecret}},
		Labels: map[string]string{
			"breakfix.dev/verify-task": task.Name,
			"breakfix.dev/role":        "publisher",
		},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(task, breakfixv1.SchemeGroupVersion.WithKind("VerifyTask"))},
	}); err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "PUBLISH_JOB_CREATE_FAILED", fmt.Sprintf("create publisher job: %v", err))
	}
	task.Status.Stage = breakfixv1.VerifyTaskPublishing
	task.Status.PublisherJobName = jobName
	task.Status.Message = "trusted publisher job created"
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *VerifyTaskReconciler) trackPublisherJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	finished, succeeded, exitCode, message, err := r.jobTerminalState(ctx, task.Status.PublisherJobName)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if !finished {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if !succeeded {
		class := breakfixv1.VerifyFailureInfrastructure
		if exitCode == 20 {
			class = breakfixv1.VerifyFailureArtifact
		}
		return r.failTask(ctx, task, class, "PUBLISH_JOB_FAILED", fmt.Sprintf("publisher job failed (exit=%d): %s", exitCode, message))
	}
	image, err := (registry.Client{Insecure: r.RegistryInsecure, Credentials: registry.Credentials{Username: r.RegistryUsername, Password: r.RegistryPassword}}).ResolveImageDigest(ctx, task.Status.StagingImage)
	if err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "STAGING_DIGEST_RESOLVE_FAILED", err.Error())
	}
	return r.createVerifierJob(ctx, task, image)
}

func (r *VerifyTaskReconciler) createVerifierJob(ctx context.Context, task *breakfixv1.VerifyTask, image string) (ctrl.Result, error) {
	jobName := verification.JobName(task.Name)
	backoff := int32(0)
	nonPrivileged := false
	if _, _, err := r.K8s.CreateOrGetJob(r.CRDNamespace, jobName, k8s.CreateJobOpts{
		Image:              r.VerifierImage,
		ContainerName:      "verifier",
		ServiceAccountName: "breakfix-verifier",
		Privileged:         &nonPrivileged,
		BackoffLimit:       &backoff,
		Env: map[string]string{
			"VERIFY_TASK_ID":        task.Name,
			"VERIFY_TASK_NAMESPACE": r.CRDNamespace,
			"VERIFY_SUBMISSION_ID":  task.Spec.Submission.ID,
		},
		SecurityContext:  trustedJobSecurityContext(),
		Resources:        verifierResources(),
		ImagePullPolicy:  corev1.PullIfNotPresent,
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: r.RegistryPullSecret}},
		Labels: map[string]string{
			"breakfix.dev/verify-task": task.Name,
			"breakfix.dev/role":        "verifier",
		},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(task, breakfixv1.SchemeGroupVersion.WithKind("VerifyTask"))},
	}); err != nil {
		return r.failTask(ctx, task, breakfixv1.VerifyFailureInfrastructure, "VERIFY_JOB_CREATE_FAILED", fmt.Sprintf("create verifier job: %v", err))
	}
	task.Status.Stage = breakfixv1.VerifyTaskVerifying
	task.Status.VerifierJobName = jobName
	task.Status.Image = image
	task.Status.Message = "trusted verifier job created"
	if _, err := r.K8s.UpdateVerifyTaskStatus(ctx, r.CRDNamespace, task); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *VerifyTaskReconciler) trackVerifierJob(ctx context.Context, task *breakfixv1.VerifyTask) (ctrl.Result, error) {
	finished, succeeded, exitCode, message, err := r.jobTerminalState(ctx, task.Status.VerifierJobName)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if !finished {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	refreshed, err := r.K8s.GetVerifyTask(ctx, r.CRDNamespace, task.Name)
	if err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if refreshed.Status.Phase != breakfixv1.VerifyTaskRunning {
		return ctrl.Result{}, nil
	}
	if succeeded {
		return r.failTask(ctx, refreshed, breakfixv1.VerifyFailureInfrastructure, "VERIFY_RESULT_MISSING", "verifier job finished without reporting result")
	}
	return r.failTask(ctx, refreshed, breakfixv1.VerifyFailureInfrastructure, "VERIFY_JOB_FAILED", fmt.Sprintf("verifier job failed (exit=%d): %s", exitCode, message))
}

func (r *VerifyTaskReconciler) issueGrant(task *breakfixv1.VerifyTask, operation verification.GrantOperation, runtime string) (string, error) {
	if len(r.VerificationGrantKey) == 0 {
		return "", fmt.Errorf("verification grant key is required")
	}
	return verification.IssueGrant(r.VerificationGrantKey, verification.Grant{
		Operation:    operation,
		TaskID:       task.Name,
		SubmissionID: task.Spec.Submission.ID,
		Runtime:      challenge.NormalizeRuntime(runtime),
		ExpiresAt:    time.Now().Add(time.Hour).Unix(),
	})
}

func (r *VerifyTaskReconciler) jobTerminalState(ctx context.Context, name string) (finished, succeeded bool, exitCode int32, message string, err error) {
	if strings.TrimSpace(name) == "" {
		return true, false, -1, "job name is missing", nil
	}
	job, err := r.K8s.Clientset().BatchV1().Jobs(r.CRDNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, false, -1, "", err
	}
	for _, condition := range job.Status.Conditions {
		switch condition.Type {
		case batchv1.JobComplete:
			return true, true, 0, condition.Message, nil
		case batchv1.JobFailed:
			return true, false, r.jobExitCode(ctx, name), condition.Message, nil
		}
	}
	return false, false, -1, "", nil
}

func (r *VerifyTaskReconciler) jobExitCode(ctx context.Context, name string) int32 {
	pods, err := r.K8s.Clientset().CoreV1().Pods(r.CRDNamespace).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + name})
	if err != nil {
		return -1
	}
	for _, pod := range pods.Items {
		for _, status := range pod.Status.ContainerStatuses {
			if status.State.Terminated != nil {
				return status.State.Terminated.ExitCode
			}
			if status.LastTerminationState.Terminated != nil {
				return status.LastTerminationState.Terminated.ExitCode
			}
		}
	}
	return -1
}

func (r *VerifyTaskReconciler) failTask(ctx context.Context, task *breakfixv1.VerifyTask, class breakfixv1.VerifyFailureClass, code, message string) (ctrl.Result, error) {
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
		Class:   class,
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

	if task.Status.Phase == breakfixv1.VerifyTaskFailed && strings.TrimSpace(task.Status.StagingImage) != "" {
		if err := (registry.Client{
			Insecure:    r.RegistryInsecure,
			Credentials: registry.Credentials{Username: r.RegistryUsername, Password: r.RegistryPassword},
		}).DeleteImage(ctx, task.Status.StagingImage); err != nil {
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

func builderBaseName(runtime string) string {
	if challenge.NormalizeRuntime(runtime) == challenge.RuntimeVCluster {
		return "breakfix-k8s-base"
	}
	return "breakfix-base"
}

func buildPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		RunAsUser:    ptr.To(int64(1000)),
		RunAsGroup:   ptr.To(int64(1000)),
		FSGroup:      ptr.To(int64(1000)),
	}
}

func buildSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		// RootlessKit creates a user namespace through newuidmap/newgidmap. Do
		// not set no_new_privs or drop the default bounding set here: both break
		// the official rootless BuildKit Kubernetes configuration.
		Privileged:      ptr.To(false),
		RunAsNonRoot:    ptr.To(true),
		RunAsUser:       ptr.To(int64(1000)),
		RunAsGroup:      ptr.To(int64(1000)),
		SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
		AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
	}
}

func trustedJobSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		Privileged:               ptr.To(false),
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func buildResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("250m"),
			corev1.ResourceMemory:           resource.MustParse("512Mi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse("2"),
			corev1.ResourceMemory:           resource.MustParse("4Gi"),
			corev1.ResourceEphemeralStorage: resource.MustParse("8Gi"),
		},
	}
}

func buildScratchVolume() corev1.Volume {
	limit := resource.MustParse("8Gi")
	return corev1.Volume{
		Name: "buildkitd",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit},
		},
	}
}

func publisherResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")},
	}
}

func verifierResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi")},
	}
}
