package controller

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"log/slog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const vclusterEnvironmentFinalizer = "breakfix.dev/vcluster-environment-cleanup"

type VClusterEnvironmentReconciler struct {
	client.Client
	K8s          *k8s.Client
	RegistryAddr string
	NS           string
	CRDNamespace string
	Cooldown     time.Duration
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
	workspacePodName := "workspace"

	if err := r.K8s.EnsureNamespace(ns); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureVCluster(ns, vclusterName); err != nil {
		env.Status.Namespace = ns
		env.Status.VClusterName = vclusterName
		env.Status.KubeconfigSecretName = kubeconfigSecret
		env.Status.WorkspacePodName = workspacePodName
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	env.Status.Phase = breakfixv1.EnvironmentProvisioning
	env.Status.Namespace = ns
	env.Status.VClusterName = vclusterName
	env.Status.KubeconfigSecretName = kubeconfigSecret
	env.Status.WorkspacePodName = workspacePodName
	env.Status.Message = "vcluster created, waiting for readiness"
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

	podName := env.Status.VClusterName + "-0"
	if err := r.K8s.WaitForPod(ns, podName, "syncer"); err != nil {
		setEnvironmentProvisioning(&env.Status.CommonEnvironmentStatus, err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	kubeconfig, err := r.readVClusterKubeconfig(ns, env.Status.VClusterName)
	if err != nil {
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	server, err := r.vclusterServerAddress(ns, env.Status.VClusterName)
	if err != nil {
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	rewriteKubeconfig, err := rewriteVClusterKubeconfig(kubeconfig, server)
	if err != nil {
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if err := r.K8s.UpsertSecret(ns, env.Status.KubeconfigSecretName, map[string][]byte{
		"config": rewriteKubeconfig,
	}); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.ensureWorkspacePod(env); err != nil {
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if err := r.K8s.WaitForPod(ns, env.Status.WorkspacePodName, "challenge"); err != nil {
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if err := r.K8s.WaitForFileInPod(ns, env.Status.WorkspacePodName, breakfixInitSentinel, 2*time.Minute); err != nil {
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	setEnvironmentReady(&env.Status.CommonEnvironmentStatus, "vcluster environment ready")
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *VClusterEnvironmentReconciler) submit(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	exitCode, output, err := r.K8s.ExecInPod(env.Status.Namespace, env.Status.WorkspacePodName, "/verify.sh")
	if err != nil {
		slog.Error("exec verify.sh", "err", err, "environment", env.Name)
	}
	setEnvironmentSubmitted(&env.Status.CommonEnvironmentStatus, exitCode, output)
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	return r.cleanup(ctx, env)
}

func (r *VClusterEnvironmentReconciler) checkCooldown(ctx context.Context, env *breakfixv1.VClusterEnvironment) (ctrl.Result, error) {
	destroy, remaining := shouldDestroyEnvironment(&env.Status.CommonEnvironmentStatus)
	if destroy {
		env.Status.Phase = breakfixv1.EnvironmentDestroyed
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return r.cleanup(ctx, env)
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
		_ = r.deleteVCluster(env.Status.Namespace, env.Status.VClusterName)
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
		_ = r.deleteVCluster(env.Status.Namespace, env.Status.VClusterName)
		_ = r.K8s.DeleteNamespace(env.Status.Namespace)

		done, err := finalizeCommonEnvironment(ctx, r.K8s, env.Status.Namespace)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !done {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
	}
	controllerutil.RemoveFinalizer(env, vclusterEnvironmentFinalizer)
	if err := r.Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}
	slog.Info("vcluster environment destroyed", "environment", env.Name)
	return ctrl.Result{}, nil
}

func (r *VClusterEnvironmentReconciler) ensureVCluster(namespace, name string) error {
	cmd := exec.Command("vcluster", "create", name, "-n", namespace, "--connect=false", "--background-proxy=false")
	var stderr bytes.Buffer
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "already exists") {
			return nil
		}
		return fmt.Errorf("vcluster create: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (r *VClusterEnvironmentReconciler) deleteVCluster(namespace, name string) error {
	cmd := exec.Command("vcluster", "delete", name, "-n", namespace)
	var stderr bytes.Buffer
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && !strings.Contains(stderr.String(), "not found") {
		return fmt.Errorf("vcluster delete: %w: %s", err, strings.TrimSpace(stderr.String()))
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

func (r *VClusterEnvironmentReconciler) ensureWorkspacePod(env *breakfixv1.VClusterEnvironment) error {
	pods, err := r.K8s.Clientset().CoreV1().Pods(env.Status.Namespace).Get(context.Background(), env.Status.WorkspacePodName, metav1.GetOptions{})
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

	return r.K8s.CreatePod(env.Status.Namespace, env.Status.WorkspacePodName, k8s.CreatePodOpts{
		Image:         imageURL,
		ChallengeID:   env.Spec.ChallengeRef,
		EnvironmentID: env.Name,
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

func (rt vclusterEnvironmentRuntime) submit(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.submit(ctx, env.(*breakfixv1.VClusterEnvironment))
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
