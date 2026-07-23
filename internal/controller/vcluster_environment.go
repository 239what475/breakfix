package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/pkg/vclustercli"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"log/slog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const vclusterEnvironmentFinalizer = "breakfix.dev/vcluster-environment-cleanup"

type VClusterEnvironmentReconciler struct {
	client.Client
	K8s           *k8s.Client
	VCluster      *vclustercli.Client
	ChartRepo     string
	ChartVersion  string
	RegistryAddr  string
	ChallengesDir string
	NS            string
	CRDNamespace  string
	Cooldown      time.Duration
}

func (r *VClusterEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var env breakfixv1.VClusterEnvironment
	if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	slog.Info("reconcile vcluster environment", "name", env.Name, "phase", env.Status.Phase, "deleting", env.DeletionTimestamp != nil)
	return reconcileCommonEnvironment(ctx, &env, vclusterEnvironmentRuntime{r: r})
}

func (r *VClusterEnvironmentReconciler) provision(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(env, vclusterEnvironmentFinalizer) {
		controllerutil.AddFinalizer(env, vclusterEnvironmentFinalizer)
		if err := r.Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
	}

	ns := k8s.UserNamespace(r.NS, env.Spec.UserRef) + "-" + env.Name
	vclusterName := "vc-" + env.Name
	kubeconfigSecret := "vc-kubeconfig"
	effectiveRuntime, err := resolveVClusterRuntime(env.Spec.VCluster)
	if err != nil {
		env.Status.Namespace = ns
		env.Status.VClusterName = vclusterName
		env.Status.KubeconfigSecretName = kubeconfigSecret
		env.Status.Profile = strings.TrimSpace(env.Spec.VCluster.Profile)
		env.Status.Version = strings.TrimSpace(env.Spec.VCluster.Version)
		markEnvironmentFailed(&env.Status.CommonEnvironmentStatus, "vcluster", "invalid_vcluster_runtime", "InvalidVClusterRuntime", err.Error(), false)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{}, nil
	}

	if err := r.K8s.EnsureNamespace(ns); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureVCluster(ctx, ns, vclusterName, effectiveRuntime); err != nil {
		env.Status.Namespace = ns
		env.Status.VClusterName = vclusterName
		env.Status.KubeconfigSecretName = kubeconfigSecret
		env.Status.Profile = effectiveRuntime.Profile
		env.Status.Version = effectiveRuntime.Version
		if vclustercli.IsCode(err, vclustercli.ErrInvalidConfig) {
			markEnvironmentFailed(&env.Status.CommonEnvironmentStatus, "vcluster", "create_invalid_config", "CreateVClusterFailed", err.Error(), false)
			_ = r.Status().Update(ctx, env)
			return ctrl.Result{}, nil
		}
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "CreateVClusterFailed", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	env.Status.Namespace = ns
	env.Status.VClusterName = vclusterName
	env.Status.KubeconfigSecretName = kubeconfigSecret
	env.Status.Profile = effectiveRuntime.Profile
	env.Status.Version = effectiveRuntime.Version
	setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "VClusterCreated", "vcluster created, waiting for readiness")
	setEnvironmentCondition(&env.Status.CommonEnvironmentStatus, breakfixv1.ConditionProvisioned, metav1.ConditionTrue, "VClusterCreated", "vcluster created")
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *VClusterEnvironmentReconciler) waitReady(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	ns := env.Status.Namespace
	if ns == "" {
		return ctrl.Result{}, fmt.Errorf("namespace missing")
	}
	workspacePodName := env.Status.WorkspacePodName
	if strings.TrimSpace(workspacePodName) == "" {
		workspacePodName = "workspace"
	}

	podName := env.Status.VClusterName + "-0"
	if err := r.K8s.WaitForPod(ns, podName, "syncer"); err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "WaitingForVCluster", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	kubeconfig, err := r.readVClusterKubeconfig(ns, env.Status.VClusterName)
	if err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "WaitingForKubeconfig", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	server, err := r.vclusterServerAddress(ns, env.Status.VClusterName)
	if err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "WaitingForVClusterService", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	rewriteKubeconfig, err := rewriteVClusterKubeconfig(kubeconfig, server)
	if err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "RewriteKubeconfigFailed", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if err := r.K8s.UpsertSecret(ns, env.Status.KubeconfigSecretName, map[string][]byte{
		"config": rewriteKubeconfig,
	}); err != nil {
		return ctrl.Result{}, err
	}
	env.Status.KubeconfigReady = true
	setEnvironmentCondition(&env.Status.CommonEnvironmentStatus, breakfixv1.ConditionKubeconfigReady, metav1.ConditionTrue, "KubeconfigReady", "kubeconfig secret is ready")

	resources, err := workspaceResourceRequirements(&env.Spec.CommonEnvironmentSpec)
	if err != nil {
		markEnvironmentFailed(&env.Status.CommonEnvironmentStatus, "workspace", "invalid_workspace_resources", "InvalidWorkspaceResources", err.Error(), false)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{}, nil
	}

	if err := r.ensureWorkspacePod(env, workspacePodName, resources); err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "WaitingForWorkspacePod", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if env.Status.WorkspacePodName != workspacePodName {
		env.Status.WorkspacePodName = workspacePodName
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.K8s.WaitForPod(ns, workspacePodName, "challenge"); err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "WaitingForWorkspacePod", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if err := r.K8s.WaitForFileInPod(ns, workspacePodName, breakfixInitSentinel, 2*time.Minute); err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, "WaitingForInitSentinel", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	setEnvironmentReady(&env.Spec.CommonEnvironmentSpec, &env.Status.CommonEnvironmentStatus, "WorkspaceReady", "vcluster environment ready")
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VClusterEnvironmentReconciler) evaluateCheckpoints(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	report, checkErr := runCheckpointEvaluation(ctx, r.K8s, r.ChallengesDir, env.Spec.ChallengeRef, env.Status.Namespace, env.Status.WorkspacePodName)
	changed := recordCheckpointStatus(&env.Status.CommonEnvironmentStatus, report, checkErr)
	if checkErr != nil {
		if changed {
			if err := r.Status().Update(ctx, env); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: checkpointInterval}, nil
	}
	if report.Passed() {
		slog.Info("environment checkpoints completed", "environment", env.Name)
		setEnvironmentCompleted(&env.Status.CommonEnvironmentStatus)
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if changed {
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: checkpointInterval}, nil
}

func (r *VClusterEnvironmentReconciler) checkCooldown(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	if !env.Spec.AutoDestroyAfterIdleOr(true) {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	destroy, remaining := shouldDestroyEnvironment(&env.Status.CommonEnvironmentStatus)
	if destroy {
		markEnvironmentDestroyed(&env.Status.CommonEnvironmentStatus, "IdleTTLExpired", "environment expired")
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return r.requestDeletion(ctx, env)
	}
	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *VClusterEnvironmentReconciler) cleanup(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	if env.Status.WorkspacePodName != "" {
		_ = r.K8s.DeletePod(env.Status.Namespace, env.Status.WorkspacePodName)
	}
	if env.Status.KubeconfigSecretName != "" {
		_ = r.K8s.DeleteSecret(env.Status.Namespace, env.Status.KubeconfigSecretName)
	}
	if env.Status.VClusterName != "" {
		if err := r.deleteVCluster(ctx, env.Status.Namespace, env.Status.VClusterName); err != nil && !vclustercli.IsCode(err, vclustercli.ErrNotFound) {
			slog.Warn("delete vcluster during cleanup", "environment", env.Name, "err", err)
		}
	}
	if env.Status.Namespace != "" {
		_ = r.K8s.DeleteNamespace(env.Status.Namespace)
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *VClusterEnvironmentReconciler) requestDeletion(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	slog.Info("requesting vcluster environment deletion", "environment", env.Name, "namespace", env.Status.Namespace)
	return requestEnvironmentDeletion(ctx, r.Client, env)
}

func (r *VClusterEnvironmentReconciler) finalCleanup(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	slog.Info("vcluster environment entering final cleanup", "name", env.Name, "namespace", env.Status.Namespace)
	if env.Status.Namespace != "" {
		_ = r.K8s.DeletePod(env.Status.Namespace, env.Status.WorkspacePodName)
		_ = r.K8s.DeleteSecret(env.Status.Namespace, env.Status.KubeconfigSecretName)
		if err := r.deleteVCluster(ctx, env.Status.Namespace, env.Status.VClusterName); err != nil && !vclustercli.IsCode(err, vclustercli.ErrNotFound) {
			slog.Warn("delete vcluster during final cleanup", "environment", env.Name, "err", err)
		}
		_ = r.K8s.DeleteNamespace(env.Status.Namespace)

		done, err := finalizeCommonEnvironment(ctx, r.K8s, env.Status.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !done {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}
	markEnvironmentDestroyed(&env.Status.CommonEnvironmentStatus, "CleanupCompleted", "vcluster environment destroyed")
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(env, vclusterEnvironmentFinalizer)
	if err := r.Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	slog.Info("vcluster environment destroyed", "environment", env.Name)
	return ctrl.Result{}, nil
}

func (r *VClusterEnvironmentReconciler) ensureVCluster(ctx context.Context, namespace, name string, runtime effectiveVClusterRuntime) error {
	if r.VCluster == nil {
		return fmt.Errorf("vcluster cli client is not configured")
	}

	valuesFile, err := writeVClusterValuesFile(runtime)
	if err != nil {
		return err
	}
	if valuesFile != "" {
		defer os.Remove(valuesFile)
	}

	opts := vclustercli.CreateOptions{
		Name:            name,
		Namespace:       namespace,
		Connect:         false,
		BackgroundProxy: false,
		ChartRepo:       r.ChartRepo,
		ChartVersion:    r.ChartVersion,
	}
	if valuesFile != "" {
		opts.ValuesFiles = []string{valuesFile}
	}

	_, err = r.VCluster.Create(ctx, opts)
	if err != nil && !vclustercli.IsCode(err, vclustercli.ErrAlreadyExists) {
		return err
	}
	return nil
}

func (r *VClusterEnvironmentReconciler) deleteVCluster(ctx context.Context, namespace, name string) error {
	if r.VCluster == nil {
		return fmt.Errorf("vcluster cli client is not configured")
	}
	_, err := r.VCluster.Delete(ctx, vclustercli.DeleteOptions{
		Name:      name,
		Namespace: namespace,
	})
	if err != nil && !vclustercli.IsCode(err, vclustercli.ErrNotFound) {
		return err
	}
	return nil
}

func (r *VClusterEnvironmentReconciler) readVClusterKubeconfig(namespace, name string) ([]byte, error) {
	secretName := "vc-" + name
	secret, err := r.K8s.Clientset().CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	config := secret.Data["config"]
	if len(config) == 0 {
		config = secret.Data["kubeconfig"]
	}
	if len(config) == 0 {
		return nil, fmt.Errorf("secret %s missing kubeconfig data", secretName)
	}
	return config, nil
}

func (r *VClusterEnvironmentReconciler) vclusterServerAddress(namespace, name string) (string, error) {
	svc, err := r.K8s.Clientset().CoreV1().Services(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get vcluster service %s/%s: %w", namespace, name, err)
	}
	if ip := strings.TrimSpace(svc.Spec.ClusterIP); ip != "" && ip != corev1.ClusterIPNone {
		return fmt.Sprintf("https://%s:443", ip), nil
	}
	return fmt.Sprintf("https://%s.%s.svc.cluster.local:443", name, namespace), nil
}

func (r *VClusterEnvironmentReconciler) ensureWorkspacePod(env *breakfixv1.VClusterEnvironment, workspacePodName string, resources corev1.ResourceRequirements) error {
	pods, err := r.K8s.Clientset().CoreV1().Pods(env.Status.Namespace).Get(context.Background(), workspacePodName, metav1.GetOptions{})
	if err == nil && pods != nil {
		return nil
	}

	imageURL := env.Spec.Image
	if strings.TrimSpace(imageURL) == "" {
		imageURL = "breakfix-k8s-base:latest"
	}
	if r.RegistryAddr != "" && !looksLikeURL(imageURL) {
		imageURL = r.RegistryAddr + "/" + imageURL
	}

	return r.K8s.CreatePod(env.Status.Namespace, workspacePodName, k8s.CreatePodOpts{
		Image:         imageURL,
		ChallengeID:   env.Spec.ChallengeRef,
		EnvironmentID: env.Name,
		Resources:     resources,
		Env: map[string]string{
			"KUBECONFIG": "/root/.kube/config",
		},
		Volumes: []corev1.Volume{{
			Name: "vcluster-kubeconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: env.Status.KubeconfigSecretName,
					Items: []corev1.KeyToPath{{
						Key:  "config",
						Path: "config",
					}},
				},
			},
		}},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "vcluster-kubeconfig",
			MountPath: "/root/.kube",
			ReadOnly:  true,
		}},
	})
}

type effectiveVClusterRuntime struct {
	Profile               string
	Version               string
	ControlPlaneCPU       string
	ControlPlaneMemory    string
	QuotaCPU              string
	QuotaMemory           string
	QuotaEphemeralStorage string
}

func resolveVClusterRuntime(spec breakfixv1.VClusterRuntimeSpec) (effectiveVClusterRuntime, error) {
	profile := strings.TrimSpace(spec.Profile)
	effective, err := vclusterProfileDefaults(profile)
	if err != nil {
		return effectiveVClusterRuntime{}, err
	}

	effective.Profile = profile
	effective.Version = strings.TrimSpace(spec.Version)
	if v := strings.TrimSpace(spec.ControlPlaneCPU); v != "" {
		effective.ControlPlaneCPU = v
	}
	if v := strings.TrimSpace(spec.ControlPlaneMemory); v != "" {
		effective.ControlPlaneMemory = v
	}
	if v := strings.TrimSpace(spec.QuotaCPU); v != "" {
		effective.QuotaCPU = v
	}
	if v := strings.TrimSpace(spec.QuotaMemory); v != "" {
		effective.QuotaMemory = v
	}
	if v := strings.TrimSpace(spec.QuotaEphemeralStorage); v != "" {
		effective.QuotaEphemeralStorage = v
	}

	if err := validateVClusterQuantity("controlPlaneCpu", effective.ControlPlaneCPU); err != nil {
		return effectiveVClusterRuntime{}, err
	}
	if err := validateVClusterQuantity("controlPlaneMemory", effective.ControlPlaneMemory); err != nil {
		return effectiveVClusterRuntime{}, err
	}
	if err := validateVClusterQuantity("quotaCpu", effective.QuotaCPU); err != nil {
		return effectiveVClusterRuntime{}, err
	}
	if err := validateVClusterQuantity("quotaMemory", effective.QuotaMemory); err != nil {
		return effectiveVClusterRuntime{}, err
	}
	if err := validateVClusterQuantity("quotaEphemeralStorage", effective.QuotaEphemeralStorage); err != nil {
		return effectiveVClusterRuntime{}, err
	}
	return effective, nil
}

func vclusterProfileDefaults(profile string) (effectiveVClusterRuntime, error) {
	switch profile {
	case "":
		return effectiveVClusterRuntime{}, nil
	case "tiny-k8s":
		return effectiveVClusterRuntime{
			ControlPlaneCPU:       "150m",
			ControlPlaneMemory:    "384Mi",
			QuotaCPU:              "2",
			QuotaMemory:           "2Gi",
			QuotaEphemeralStorage: "8Gi",
		}, nil
	case "default-k8s":
		return effectiveVClusterRuntime{
			ControlPlaneCPU:       "250m",
			ControlPlaneMemory:    "512Mi",
			QuotaCPU:              "4",
			QuotaMemory:           "4Gi",
			QuotaEphemeralStorage: "12Gi",
		}, nil
	case "troubleshooting-k8s":
		return effectiveVClusterRuntime{
			ControlPlaneCPU:       "400m",
			ControlPlaneMemory:    "768Mi",
			QuotaCPU:              "8",
			QuotaMemory:           "8Gi",
			QuotaEphemeralStorage: "20Gi",
		}, nil
	default:
		return effectiveVClusterRuntime{}, fmt.Errorf("unsupported vcluster profile %q", profile)
	}
}

func validateVClusterQuantity(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, err := resource.ParseQuantity(value); err != nil {
		return fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	return nil
}

func writeVClusterValuesFile(runtime effectiveVClusterRuntime) (string, error) {
	values := buildVClusterValues(runtime)
	if len(values) == 0 {
		return "", nil
	}

	data, err := yaml.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("marshal vcluster values: %w", err)
	}
	f, err := os.CreateTemp("", "breakfix-vcluster-values-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create vcluster values file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", fmt.Errorf("write vcluster values file: %w", err)
	}
	return f.Name(), nil
}

func buildVClusterValues(runtime effectiveVClusterRuntime) map[string]any {
	values := map[string]any{}

	if runtime.Version != "" {
		setNested(values, true, "controlPlane", "distro", "k8s", "enabled")
		setNested(values, runtime.Version, "controlPlane", "distro", "k8s", "version")
	}
	if runtime.ControlPlaneCPU != "" {
		setNested(values, runtime.ControlPlaneCPU, "controlPlane", "statefulSet", "resources", "requests", "cpu")
		setNested(values, runtime.ControlPlaneCPU, "controlPlane", "statefulSet", "resources", "limits", "cpu")
	}
	if runtime.ControlPlaneMemory != "" {
		setNested(values, runtime.ControlPlaneMemory, "controlPlane", "statefulSet", "resources", "requests", "memory")
		setNested(values, runtime.ControlPlaneMemory, "controlPlane", "statefulSet", "resources", "limits", "memory")
	}
	if runtime.QuotaCPU != "" || runtime.QuotaMemory != "" || runtime.QuotaEphemeralStorage != "" {
		setNested(values, true, "policies", "resourceQuota", "enabled")
	}
	if runtime.QuotaCPU != "" {
		setNested(values, runtime.QuotaCPU, "policies", "resourceQuota", "quota", "requests.cpu")
		setNested(values, runtime.QuotaCPU, "policies", "resourceQuota", "quota", "limits.cpu")
	}
	if runtime.QuotaMemory != "" {
		setNested(values, runtime.QuotaMemory, "policies", "resourceQuota", "quota", "requests.memory")
		setNested(values, runtime.QuotaMemory, "policies", "resourceQuota", "quota", "limits.memory")
	}
	if runtime.QuotaEphemeralStorage != "" {
		setNested(values, runtime.QuotaEphemeralStorage, "policies", "resourceQuota", "quota", "requests.ephemeral-storage")
		setNested(values, runtime.QuotaEphemeralStorage, "policies", "resourceQuota", "quota", "limits.ephemeral-storage")
	}

	return values
}

func setNested(root map[string]any, value any, path ...string) {
	current := root
	for i, key := range path {
		if i == len(path)-1 {
			current[key] = value
			return
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[key] = next
		}
		current = next
	}
}

func rewriteVClusterKubeconfig(raw []byte, server string) ([]byte, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	for name, cluster := range cfg.Clusters {
		if cluster == nil {
			cfg.Clusters[name] = &clientcmdapi.Cluster{Server: server}
			continue
		}
		cluster.Server = server
	}
	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("write kubeconfig: %w", err)
	}
	return out, nil
}

func (r *VClusterEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: vclusterEnvironmentMaxConcurrentReconciles}).
		For(&breakfixv1.VClusterEnvironment{}).
		Complete(r)
}

type vclusterEnvironmentRuntime struct {
	r *VClusterEnvironmentReconciler
}

func (rt vclusterEnvironmentRuntime) provision(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.provision(ctx, env.(*breakfixv1.VClusterEnvironment))
}

func (rt vclusterEnvironmentRuntime) waitReady(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.waitReady(ctx, env.(*breakfixv1.VClusterEnvironment))
}

func (rt vclusterEnvironmentRuntime) evaluateCheckpoints(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.evaluateCheckpoints(ctx, env.(*breakfixv1.VClusterEnvironment))
}

func (rt vclusterEnvironmentRuntime) handleDraining(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.checkCooldown(ctx, env.(*breakfixv1.VClusterEnvironment))
}

func (rt vclusterEnvironmentRuntime) cleanup(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.cleanup(ctx, env.(*breakfixv1.VClusterEnvironment))
}

func (rt vclusterEnvironmentRuntime) finalCleanup(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.finalCleanup(ctx, env.(*breakfixv1.VClusterEnvironment))
}

func (rt vclusterEnvironmentRuntime) requestDeletion(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.requestDeletion(ctx, env.(*breakfixv1.VClusterEnvironment))
}
