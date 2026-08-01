package controller

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/vclustercli"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	maxVClusterReleaseNameLength      = 52
	vk8sEnvironmentUIDAnnotation      = "breakfix.dev/environment-uid"
	vk8sEnvironmentRevisionAnnotation = "breakfix.dev/source-revision"
	vk8sRuntimeLabel                  = "breakfix.dev/runtime"
	vk8sRuntimeLabelValue             = "k8s"
	vk8sRuntimeServiceAccount         = "breakfix-runtime"
	vk8sInitSentinel                  = "/var/lib/breakfix/.initialized"
	vk8sChallengeRoot                 = "/opt/breakfix/challenge/k8s"
)

type VK8sEnvironmentIdentity struct {
	Namespace            string
	VClusterName         string
	KubeconfigSecretName string
	TerminalPodName      string
}

type VK8sProvisionRequest struct {
	EnvironmentUID string
	Revision       string
	Purpose        breakfixv1.EnvironmentPurpose
	Identity       VK8sEnvironmentIdentity
	Runtime        breakfixv1.VK8sRuntimeSnapshot
}

type VK8sInitializationObservation struct {
	Complete bool
	Failed   bool
	ExitCode int
	Message  string
}

type VK8sEnvironmentObservation struct {
	ControlPlaneReady bool
	KubeconfigReady   bool
	TerminalReady     bool
	Initialization    VK8sInitializationObservation
}

type VK8sExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type VK8sEnvironmentProvider interface {
	EnvironmentIdentity(environmentUID string) (VK8sEnvironmentIdentity, error)
	Provision(context.Context, VK8sProvisionRequest) (VK8sEnvironmentObservation, error)
	Delete(context.Context, VK8sProvisionRequest) (bool, error)
	ExecTerminal(context.Context, VK8sProvisionRequest, []string) (VK8sExecResult, error)
}

type vclusterCommand interface {
	Create(context.Context, vclustercli.CreateOptions) (*vclustercli.Result, error)
	Delete(context.Context, vclustercli.DeleteOptions) (*vclustercli.Result, error)
}

type kubernetesVK8sProvider struct {
	k8s                        *k8s.Client
	vcluster                   vclusterCommand
	namespacePrefix            string
	controlNamespace           string
	registryPullSecret         string
	verificationServiceAccount string
	chartRepo                  string
	chartVersion               string
}

func (p *kubernetesVK8sProvider) EnvironmentIdentity(environmentUID string) (VK8sEnvironmentIdentity, error) {
	environmentUID = strings.TrimSpace(environmentUID)
	if environmentUID == "" {
		return VK8sEnvironmentIdentity{}, fmt.Errorf("environment UID is required")
	}
	//nolint:gosec // KubeconfigSecretName is a Kubernetes object name, not credential material.
	return VK8sEnvironmentIdentity{
		Namespace:            k8s.DNSLabelName(p.namespacePrefix+"-vk8s", environmentUID),
		VClusterName:         k8s.DNSLabelNameWithLimit(maxVClusterReleaseNameLength, "vc", environmentUID),
		KubeconfigSecretName: "breakfix-vk8s-kubeconfig",
		TerminalPodName:      "terminal",
	}, nil
}

func (p *kubernetesVK8sProvider) Provision(ctx context.Context, request VK8sProvisionRequest) (VK8sEnvironmentObservation, error) {
	if p.k8s == nil || p.vcluster == nil {
		return VK8sEnvironmentObservation{}, fmt.Errorf("VK8s provider is not configured")
	}
	if err := p.ensureNamespace(ctx, request); err != nil {
		return VK8sEnvironmentObservation{}, err
	}
	if strings.TrimSpace(p.registryPullSecret) != "" {
		if err := p.k8s.EnsureImagePullSecret(ctx, p.controlNamespace, request.Identity.Namespace, p.registryPullSecret); err != nil {
			return VK8sEnvironmentObservation{}, fmt.Errorf("ensure registry pull secret: %w", err)
		}
	}
	if err := p.k8s.EnsureRuntimeServiceAccount(ctx, request.Identity.Namespace, vk8sRuntimeServiceAccount, p.registryPullSecret); err != nil {
		return VK8sEnvironmentObservation{}, fmt.Errorf("ensure runtime service account: %w", err)
	}
	if request.Purpose == breakfixv1.EnvironmentPurposeVerification {
		if err := p.k8s.EnsureVerificationWorkspaceExecAccess(request.Identity.Namespace, p.controlNamespace, p.verificationServiceAccount); err != nil {
			return VK8sEnvironmentObservation{}, fmt.Errorf("ensure verification terminal access: %w", err)
		}
	}
	if err := p.ensureVCluster(ctx, request); err != nil {
		return VK8sEnvironmentObservation{}, err
	}

	observation := VK8sEnvironmentObservation{}
	ready, err := p.controlPlaneReady(ctx, request.Identity)
	if err != nil {
		return observation, err
	}
	observation.ControlPlaneReady = ready
	if !ready {
		return observation, nil
	}

	kubeconfig, err := p.readKubeconfig(ctx, request.Identity)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return observation, nil
		}
		return observation, err
	}
	server, err := p.vclusterServerAddress(ctx, request.Identity)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return observation, nil
		}
		return observation, err
	}
	kubeconfig, err = rewriteVClusterKubeconfig(kubeconfig, server)
	if err != nil {
		return observation, fmt.Errorf("rewrite vcluster kubeconfig: %w", err)
	}
	if err := p.upsertKubeconfig(ctx, request, kubeconfig); err != nil {
		return observation, err
	}
	observation.KubeconfigReady = true

	if err := p.ensureTerminal(ctx, request); err != nil {
		return observation, err
	}
	terminal, err := p.observeTerminal(ctx, request)
	if err != nil {
		return observation, err
	}
	observation.TerminalReady = terminal.Complete && !terminal.Failed
	observation.Initialization = terminal
	return observation, nil
}

func (p *kubernetesVK8sProvider) Delete(ctx context.Context, request VK8sProvisionRequest) (bool, error) {
	if p.k8s == nil || p.vcluster == nil {
		return false, fmt.Errorf("VK8s provider is not configured")
	}
	namespaces := p.k8s.Clientset().CoreV1().Namespaces()
	namespace, err := namespaces.Get(ctx, request.Identity.Namespace, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get VK8s namespace: %w", err)
	}
	if err := verifyVK8sNamespaceOwner(namespace, request); err != nil {
		return false, err
	}
	_, err = p.vcluster.Delete(ctx, vclustercli.DeleteOptions{
		Name: request.Identity.VClusterName, Namespace: request.Identity.Namespace,
	})
	if err != nil && !vclustercli.IsCode(err, vclustercli.ErrNotFound) {
		return false, fmt.Errorf("delete vcluster: %w", err)
	}
	uid := namespace.UID
	if err := namespaces.Delete(ctx, namespace.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !k8serrors.IsNotFound(err) {
		return false, fmt.Errorf("delete VK8s namespace: %w", err)
	}
	return false, nil
}

func (p *kubernetesVK8sProvider) ExecTerminal(ctx context.Context, request VK8sProvisionRequest, command []string) (VK8sExecResult, error) {
	if p.k8s == nil {
		return VK8sExecResult{}, fmt.Errorf("VK8s provider is not configured")
	}
	if len(command) == 0 {
		return VK8sExecResult{}, fmt.Errorf("terminal command is required")
	}
	result, err := p.k8s.ExecInPodStreamsContext(ctx, request.Identity.Namespace, request.Identity.TerminalPodName, 64*1024, command...)
	return VK8sExecResult{ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}, err
}

func (p *kubernetesVK8sProvider) ensureNamespace(ctx context.Context, request VK8sProvisionRequest) error {
	namespaces := p.k8s.Clientset().CoreV1().Namespaces()
	namespace, err := namespaces.Get(ctx, request.Identity.Namespace, metav1.GetOptions{})
	if err == nil {
		return verifyVK8sNamespaceOwner(namespace, request)
	}
	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("get VK8s namespace: %w", err)
	}
	_, err = namespaces.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: request.Identity.Namespace,
		Labels: map[string]string{
			"app.kubernetes.io/part-of": "breakfix",
			vk8sRuntimeLabel:            vk8sRuntimeLabelValue,
		},
		Annotations: map[string]string{
			vk8sEnvironmentUIDAnnotation:      request.EnvironmentUID,
			vk8sEnvironmentRevisionAnnotation: request.Revision,
		},
	}}, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create VK8s namespace: %w", err)
	}
	return nil
}

func verifyVK8sNamespaceOwner(namespace *corev1.Namespace, request VK8sProvisionRequest) error {
	if namespace == nil || namespace.Annotations[vk8sEnvironmentUIDAnnotation] != request.EnvironmentUID || namespace.Annotations[vk8sEnvironmentRevisionAnnotation] != request.Revision || namespace.Labels[vk8sRuntimeLabel] != vk8sRuntimeLabelValue {
		return fmt.Errorf("VK8s namespace %q has different ownership metadata", request.Identity.Namespace)
	}
	return nil
}

func (p *kubernetesVK8sProvider) ensureVCluster(ctx context.Context, request VK8sProvisionRequest) error {
	_, err := p.k8s.Clientset().AppsV1().StatefulSets(request.Identity.Namespace).Get(ctx, request.Identity.VClusterName, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("get vcluster StatefulSet: %w", err)
	}
	valuesFile, err := writeVK8sValuesFile(request.Runtime)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(valuesFile) }()
	_, err = p.vcluster.Create(ctx, vclustercli.CreateOptions{
		Name: request.Identity.VClusterName, Namespace: request.Identity.Namespace,
		Connect: false, BackgroundProxy: false, ChartRepo: p.chartRepo,
		ChartVersion: p.chartVersion, ValuesFiles: []string{valuesFile},
	})
	if err != nil && !vclustercli.IsCode(err, vclustercli.ErrAlreadyExists) {
		return fmt.Errorf("create vcluster: %w", err)
	}
	return nil
}

func (p *kubernetesVK8sProvider) controlPlaneReady(ctx context.Context, identity VK8sEnvironmentIdentity) (bool, error) {
	pod, err := p.k8s.Clientset().CoreV1().Pods(identity.Namespace).Get(ctx, identity.VClusterName+"-0", metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get vcluster control plane pod: %w", err)
	}
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return false, fmt.Errorf("vcluster control plane pod entered %s", pod.Status.Phase)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "syncer" {
			continue
		}
		if status.State.Terminated != nil {
			return false, fmt.Errorf("vcluster syncer terminated with exit code %d: %s", status.State.Terminated.ExitCode, strings.TrimSpace(status.State.Terminated.Message))
		}
		if status.State.Waiting != nil && terminalPodWaitingReason(status.State.Waiting.Reason) {
			return false, fmt.Errorf("vcluster syncer cannot start: %s: %s", status.State.Waiting.Reason, strings.TrimSpace(status.State.Waiting.Message))
		}
		return status.Ready, nil
	}
	return false, nil
}

func (p *kubernetesVK8sProvider) readKubeconfig(ctx context.Context, identity VK8sEnvironmentIdentity) ([]byte, error) {
	secret, err := p.k8s.Clientset().CoreV1().Secrets(identity.Namespace).Get(ctx, "vc-"+identity.VClusterName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	config := secret.Data["config"]
	if len(config) == 0 {
		config = secret.Data["kubeconfig"]
	}
	if len(config) == 0 {
		return nil, fmt.Errorf("vcluster kubeconfig Secret contains no config")
	}
	return config, nil
}

func (p *kubernetesVK8sProvider) vclusterServerAddress(ctx context.Context, identity VK8sEnvironmentIdentity) (string, error) {
	_, err := p.k8s.Clientset().CoreV1().Services(identity.Namespace).Get(ctx, identity.VClusterName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return vclusterServiceAddress(identity), nil
}

func vclusterServiceAddress(identity VK8sEnvironmentIdentity) string {
	// vcluster's serving certificate covers the service short name and namespace,
	// but not the fully-qualified .svc.cluster.local name.
	return fmt.Sprintf("https://%s.%s:443", identity.VClusterName, identity.Namespace)
}

func (p *kubernetesVK8sProvider) upsertKubeconfig(ctx context.Context, request VK8sProvisionRequest, config []byte) error {
	secrets := p.k8s.Clientset().CoreV1().Secrets(request.Identity.Namespace)
	secret, err := secrets.Get(ctx, request.Identity.KubeconfigSecretName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name: request.Identity.KubeconfigSecretName, Namespace: request.Identity.Namespace,
			Annotations: map[string]string{vk8sEnvironmentUIDAnnotation: request.EnvironmentUID, vk8sEnvironmentRevisionAnnotation: request.Revision},
		}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"config": append([]byte(nil), config...)}}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return fmt.Errorf("get terminal kubeconfig Secret: %w", err)
	}
	if secret.Annotations[vk8sEnvironmentUIDAnnotation] != request.EnvironmentUID || secret.Annotations[vk8sEnvironmentRevisionAnnotation] != request.Revision {
		return fmt.Errorf("terminal kubeconfig Secret has different ownership metadata")
	}
	secret.Data = map[string][]byte{"config": append([]byte(nil), config...)}
	_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func (p *kubernetesVK8sProvider) ensureTerminal(ctx context.Context, request VK8sProvisionRequest) error {
	pods := p.k8s.Clientset().CoreV1().Pods(request.Identity.Namespace)
	pod, err := pods.Get(ctx, request.Identity.TerminalPodName, metav1.GetOptions{})
	if err == nil {
		if pod.Annotations[vk8sEnvironmentUIDAnnotation] != request.EnvironmentUID || pod.Annotations[vk8sEnvironmentRevisionAnnotation] != request.Revision || pod.Spec.ServiceAccountName != vk8sRuntimeServiceAccount || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != request.Runtime.ImageDigest {
			return fmt.Errorf("VK8s terminal pod differs from immutable runtime snapshot")
		}
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("get VK8s terminal pod: %w", err)
	}
	resources, err := vk8sWorkloadResources(request.Runtime.Resources)
	if err != nil {
		return err
	}
	mode := int32(0o600)
	_, err = pods.Create(ctx, newVK8sTerminalPod(request, resources, mode), metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create VK8s terminal pod: %w", err)
	}
	return nil
}

func newVK8sTerminalPod(request VK8sProvisionRequest, resources corev1.ResourceRequirements, kubeconfigMode int32) *corev1.Pod {
	automount := false
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: request.Identity.TerminalPodName, Namespace: request.Identity.Namespace,
			Labels:      map[string]string{"app.kubernetes.io/part-of": "breakfix", vk8sRuntimeLabel: vk8sRuntimeLabelValue},
			Annotations: map[string]string{vk8sEnvironmentUIDAnnotation: request.EnvironmentUID, vk8sEnvironmentRevisionAnnotation: request.Revision},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &automount,
			RestartPolicy:                corev1.RestartPolicyNever,
			ServiceAccountName:           vk8sRuntimeServiceAccount,
			Containers: []corev1.Container{{
				Name: "challenge", Image: request.Runtime.ImageDigest, ImagePullPolicy: corev1.PullIfNotPresent,
				Resources: resources,
				Env: []corev1.EnvVar{
					{Name: "KUBECONFIG", Value: "/root/.kube/config"},
					{Name: "BREAKFIX_GENERATE_SCRIPT", Value: vk8sChallengeRoot + "/generate.sh"},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "kubeconfig", MountPath: "/root/.kube", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "kubeconfig", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: request.Identity.KubeconfigSecretName, DefaultMode: &kubeconfigMode,
				Items: []corev1.KeyToPath{{Key: "config", Path: "config"}},
			}}}},
		},
	}
}

func (p *kubernetesVK8sProvider) observeTerminal(ctx context.Context, request VK8sProvisionRequest) (VK8sInitializationObservation, error) {
	pod, err := p.k8s.Clientset().CoreV1().Pods(request.Identity.Namespace).Get(ctx, request.Identity.TerminalPodName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return VK8sInitializationObservation{}, nil
	}
	if err != nil {
		return VK8sInitializationObservation{}, fmt.Errorf("get VK8s terminal pod: %w", err)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "challenge" {
			continue
		}
		if status.State.Waiting != nil && terminalPodWaitingReason(status.State.Waiting.Reason) {
			return VK8sInitializationObservation{}, fmt.Errorf("VK8s terminal cannot start: %s: %s", status.State.Waiting.Reason, strings.TrimSpace(status.State.Waiting.Message))
		}
		if status.State.Terminated != nil {
			return VK8sInitializationObservation{Failed: true, ExitCode: int(status.State.Terminated.ExitCode), Message: terminalTerminationMessage(status.State.Terminated)}, nil
		}
		if !status.Ready {
			return VK8sInitializationObservation{}, nil
		}
		result, err := p.k8s.ExecInPodStreamsContext(ctx, request.Identity.Namespace, request.Identity.TerminalPodName, 4096, "test", "-f", vk8sInitSentinel)
		if err != nil {
			return VK8sInitializationObservation{}, err
		}
		if result.ExitCode == 0 {
			return VK8sInitializationObservation{Complete: true}, nil
		}
		if result.ExitCode == 1 {
			return VK8sInitializationObservation{}, nil
		}
		return VK8sInitializationObservation{}, fmt.Errorf("inspect VK8s runtime initializer: exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return VK8sInitializationObservation{}, nil
}

func terminalPodWaitingReason(reason string) bool {
	switch reason {
	case "CreateContainerConfigError", "CreateContainerError", "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "RunContainerError":
		return true
	default:
		return false
	}
}

func terminalTerminationMessage(state *corev1.ContainerStateTerminated) string {
	if state == nil {
		return "challenge terminal terminated"
	}
	parts := []string{fmt.Sprintf("runtime initialization exited with %d", state.ExitCode)}
	if value := strings.TrimSpace(state.Reason); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(state.Message); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, ": ")
}

func vk8sWorkloadResources(snapshot breakfixv1.VK8sResourceSnapshot) (corev1.ResourceRequirements, error) {
	cpu, err := resource.ParseQuantity(snapshot.WorkloadCPU)
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("parse workload CPU: %w", err)
	}
	memory, err := resource.ParseQuantity(snapshot.WorkloadMemory)
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("parse workload memory: %w", err)
	}
	ephemeralStorage, err := resource.ParseQuantity(snapshot.WorkloadEphemeralStorage)
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("parse workload ephemeral storage: %w", err)
	}
	resources := corev1.ResourceList{corev1.ResourceCPU: cpu, corev1.ResourceMemory: memory, corev1.ResourceEphemeralStorage: ephemeralStorage}
	return corev1.ResourceRequirements{Requests: resources.DeepCopy(), Limits: resources}, nil
}

func writeVK8sValuesFile(runtime breakfixv1.VK8sRuntimeSnapshot) (string, error) {
	data, err := yaml.Marshal(buildVK8sValues(runtime))
	if err != nil {
		return "", fmt.Errorf("marshal vcluster values: %w", err)
	}
	file, err := os.CreateTemp("", "breakfix-vk8s-values-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create vcluster values file: %w", err)
	}
	path := file.Name()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write vcluster values file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close vcluster values file: %w", err)
	}
	return path, nil
}

func buildVK8sValues(runtime breakfixv1.VK8sRuntimeSnapshot) map[string]any {
	values := map[string]any{}
	setNestedValue(values, true, "controlPlane", "distro", "k8s", "enabled")
	setNestedValue(values, runtime.Version, "controlPlane", "distro", "k8s", "version")
	setNestedValue(values, runtime.Resources.ControlPlaneCPU, "controlPlane", "statefulSet", "resources", "requests", "cpu")
	setNestedValue(values, runtime.Resources.ControlPlaneCPU, "controlPlane", "statefulSet", "resources", "limits", "cpu")
	setNestedValue(values, runtime.Resources.ControlPlaneMemory, "controlPlane", "statefulSet", "resources", "requests", "memory")
	setNestedValue(values, runtime.Resources.ControlPlaneMemory, "controlPlane", "statefulSet", "resources", "limits", "memory")
	setNestedValue(values, runtime.Resources.ControlPlaneEphemeralStorage, "controlPlane", "statefulSet", "resources", "requests", "ephemeral-storage")
	setNestedValue(values, runtime.Resources.ControlPlaneEphemeralStorage, "controlPlane", "statefulSet", "resources", "limits", "ephemeral-storage")
	setNestedValue(values, true, "policies", "resourceQuota", "enabled")
	setNestedValue(values, runtime.Resources.QuotaCPU, "policies", "resourceQuota", "quota", "requests.cpu")
	setNestedValue(values, runtime.Resources.QuotaCPU, "policies", "resourceQuota", "quota", "limits.cpu")
	setNestedValue(values, runtime.Resources.QuotaMemory, "policies", "resourceQuota", "quota", "requests.memory")
	setNestedValue(values, runtime.Resources.QuotaMemory, "policies", "resourceQuota", "quota", "limits.memory")
	setNestedValue(values, runtime.Resources.QuotaEphemeralStorage, "policies", "resourceQuota", "quota", "requests.ephemeral-storage")
	setNestedValue(values, runtime.Resources.QuotaEphemeralStorage, "policies", "resourceQuota", "quota", "limits.ephemeral-storage")
	setNestedValue(values, true, "policies", "limitRange", "enabled")
	for _, target := range []string{"default", "defaultRequest"} {
		setNestedValue(values, runtime.Resources.WorkloadCPU, "policies", "limitRange", target, "cpu")
		setNestedValue(values, runtime.Resources.WorkloadMemory, "policies", "limitRange", target, "memory")
		setNestedValue(values, runtime.Resources.WorkloadEphemeralStorage, "policies", "limitRange", target, "ephemeral-storage")
	}
	return values
}

func setNestedValue(root map[string]any, value any, path ...string) {
	current := root
	for index, key := range path {
		if index == len(path)-1 {
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
	config, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	for name, cluster := range config.Clusters {
		if cluster == nil {
			config.Clusters[name] = &clientcmdapi.Cluster{Server: server}
			continue
		}
		cluster.Server = server
	}
	for _, context := range config.Contexts {
		if context != nil {
			context.Namespace = "default"
		}
	}
	output, err := clientcmd.Write(*config)
	if err != nil {
		return nil, fmt.Errorf("write kubeconfig: %w", err)
	}
	return output, nil
}
