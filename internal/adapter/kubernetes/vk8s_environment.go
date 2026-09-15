package kubernetes

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/vcluster"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	maxVClusterReleaseNameLength      = 52
	vk8sEnvironmentUIDAnnotation      = "breakfix.dev/environment-uid"
	vk8sEnvironmentRevisionAnnotation = "breakfix.dev/source-revision"
	vk8sRuntimeLabel                  = "breakfix.dev/runtime"
	vk8sRuntimeLabelValue             = "k8s"
	vk8sTerminalComponentLabel        = "breakfix.dev/component"
	vk8sTerminalComponentValue        = "vk8s-management-terminal"
	vk8sTerminalNetworkPolicyName     = "breakfix-vk8s-terminal"
	vk8sRuntimeServiceAccount         = "breakfix-runtime"
	vk8sInitSentinel                  = "/var/lib/breakfix/.initialized"
)

type vclusterCommand interface {
	Create(context.Context, vcluster.CreateOptions) (*vcluster.Result, error)
	Delete(context.Context, vcluster.DeleteOptions) (*vcluster.Result, error)
}

// VK8sEnvironmentProviderConfig contains the platform-owned dependencies for
// virtual Kubernetes environments. Individual CRDs carry only their immutable
// runtime snapshots.
type VK8sEnvironmentProviderConfig struct {
	NamespacePrefix            string
	ControlNamespace           string
	RegistryPullSecret         string
	VerificationServiceAccount string
	ChartRepo                  string
	ChartVersion               string
}

type vk8sEnvironmentProvider struct {
	k8s                        *Client
	vcluster                   vclusterCommand
	namespacePrefix            string
	controlNamespace           string
	registryPullSecret         string
	verificationServiceAccount string
	chartRepo                  string
	chartVersion               string
}

func NewVK8sEnvironmentProvider(k8s *Client, vclusterClient *vcluster.Client, config VK8sEnvironmentProviderConfig) environment.VK8sProvider {
	return &vk8sEnvironmentProvider{
		k8s:                        k8s,
		vcluster:                   vclusterClient,
		namespacePrefix:            config.NamespacePrefix,
		controlNamespace:           config.ControlNamespace,
		registryPullSecret:         config.RegistryPullSecret,
		verificationServiceAccount: config.VerificationServiceAccount,
		chartRepo:                  config.ChartRepo,
		chartVersion:               config.ChartVersion,
	}
}

func (p *vk8sEnvironmentProvider) Identity(environmentUID string) (environment.VK8sEnvironmentIdentity, error) {
	environmentUID = strings.TrimSpace(environmentUID)
	if environmentUID == "" {
		return environment.VK8sEnvironmentIdentity{}, fmt.Errorf("environment UID is required")
	}
	//nolint:gosec // KubeconfigSecretName is a Kubernetes object name, not credential material.
	return environment.VK8sEnvironmentIdentity{
		Namespace:            DNSLabelName(p.namespacePrefix+"-vk8s", environmentUID),
		VClusterName:         DNSLabelNameWithLimit(maxVClusterReleaseNameLength, "vc", environmentUID),
		KubeconfigSecretName: "breakfix-vk8s-kubeconfig",
		TerminalPodName:      "terminal",
	}, nil
}

func (p *vk8sEnvironmentProvider) Provision(ctx context.Context, request environment.VK8sProvisionRequest) (environment.VK8sEnvironmentObservation, error) {
	if p.k8s == nil || p.vcluster == nil {
		return environment.VK8sEnvironmentObservation{}, fmt.Errorf("VK8s provider is not configured")
	}
	if err := p.ensureNamespace(ctx, request); err != nil {
		return environment.VK8sEnvironmentObservation{}, err
	}
	if strings.TrimSpace(p.registryPullSecret) != "" {
		if err := p.k8s.EnsureImagePullSecret(ctx, p.controlNamespace, request.Identity.Namespace, p.registryPullSecret); err != nil {
			return environment.VK8sEnvironmentObservation{}, fmt.Errorf("ensure registry pull secret: %w", err)
		}
	}
	if err := p.k8s.EnsureRuntimeServiceAccount(ctx, request.Identity.Namespace, vk8sRuntimeServiceAccount, p.registryPullSecret); err != nil {
		return environment.VK8sEnvironmentObservation{}, fmt.Errorf("ensure runtime service account: %w", err)
	}
	if request.Purpose == environment.PurposeVerification {
		if err := p.k8s.EnsureVerificationWorkspaceExecAccess(request.Identity.Namespace, p.controlNamespace, p.verificationServiceAccount); err != nil {
			return environment.VK8sEnvironmentObservation{}, fmt.Errorf("ensure verification terminal access: %w", err)
		}
	}
	if err := p.ensureVCluster(ctx, request); err != nil {
		return environment.VK8sEnvironmentObservation{}, err
	}
	if err := p.ensureTerminalNetworkPolicy(ctx, request); err != nil {
		return environment.VK8sEnvironmentObservation{}, err
	}

	observation := environment.VK8sEnvironmentObservation{}
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

func (p *vk8sEnvironmentProvider) Delete(ctx context.Context, request environment.VK8sProvisionRequest) (bool, error) {
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
	_, err = p.vcluster.Delete(ctx, vcluster.DeleteOptions{
		Name: request.Identity.VClusterName, Namespace: request.Identity.Namespace,
	})
	if err != nil && !vcluster.IsCode(err, vcluster.ErrNotFound) {
		return false, fmt.Errorf("delete vcluster: %w", err)
	}
	uid := namespace.UID
	if err := namespaces.Delete(ctx, namespace.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !k8serrors.IsNotFound(err) {
		return false, fmt.Errorf("delete VK8s namespace: %w", err)
	}
	return false, nil
}

func (p *vk8sEnvironmentProvider) ExecuteTerminal(ctx context.Context, request environment.VK8sProvisionRequest, command []string) (environment.ExecutionResult, error) {
	if p.k8s == nil {
		return environment.ExecutionResult{}, fmt.Errorf("VK8s provider is not configured")
	}
	if len(command) == 0 {
		return environment.ExecutionResult{}, fmt.Errorf("terminal command is required")
	}
	result, err := p.k8s.ExecInPodStreamsContext(ctx, request.Identity.Namespace, request.Identity.TerminalPodName, 64*1024, command...)
	return environment.ExecutionResult{ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}, err
}

func (p *vk8sEnvironmentProvider) ensureNamespace(ctx context.Context, request environment.VK8sProvisionRequest) error {
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

func verifyVK8sNamespaceOwner(namespace *corev1.Namespace, request environment.VK8sProvisionRequest) error {
	if namespace == nil || namespace.Annotations[vk8sEnvironmentUIDAnnotation] != request.EnvironmentUID || namespace.Annotations[vk8sEnvironmentRevisionAnnotation] != request.Revision || namespace.Labels[vk8sRuntimeLabel] != vk8sRuntimeLabelValue {
		return fmt.Errorf("VK8s namespace %q has different ownership metadata", request.Identity.Namespace)
	}
	return nil
}

func (p *vk8sEnvironmentProvider) ensureVCluster(ctx context.Context, request environment.VK8sProvisionRequest) error {
	_, err := p.k8s.Clientset().AppsV1().StatefulSets(request.Identity.Namespace).Get(ctx, request.Identity.VClusterName, metav1.GetOptions{})
	upgrade := false
	if err == nil {
		matched, checkErr := p.vclusterNetworkPoliciesMatch(ctx, request)
		if checkErr != nil {
			return checkErr
		}
		if matched {
			return nil
		}
		upgrade = true
	}
	if err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("get vcluster StatefulSet: %w", err)
	}
	valuesFile, err := writeVK8sValuesFile(request.Runtime)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(valuesFile) }()
	_, err = p.vcluster.Create(ctx, vcluster.CreateOptions{
		Name: request.Identity.VClusterName, Namespace: request.Identity.Namespace,
		Connect: false, BackgroundProxy: false, Upgrade: upgrade, ChartRepo: p.chartRepo,
		ChartVersion: p.chartVersion, ValuesFiles: []string{valuesFile},
	})
	if err != nil {
		if !upgrade && vcluster.IsCode(err, vcluster.ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("create or upgrade vcluster: %w", err)
	}
	return nil
}

// vclusterNetworkPoliciesMatch checks the stable chart contract that protects
// synced workload Pods. Existing virtual clusters are upgraded when their
// workload policy is missing or has a different public-egress boundary.
func (p *vk8sEnvironmentProvider) vclusterNetworkPoliciesMatch(ctx context.Context, request environment.VK8sProvisionRequest) (bool, error) {
	if err := request.Runtime.Network.Validate(); err != nil {
		return false, fmt.Errorf("validate VK8s network policy: %w", err)
	}
	policies := p.k8s.Clientset().NetworkingV1().NetworkPolicies(request.Identity.Namespace)
	controlPlane, err := policies.Get(ctx, "vc-cp-"+request.Identity.VClusterName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get vcluster control-plane network policy: %w", err)
	}
	if !vclusterControlPlaneNetworkPolicyMatches(controlPlane, request.Identity.VClusterName) {
		return false, nil
	}
	workload, err := policies.Get(ctx, "vc-work-"+request.Identity.VClusterName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get vcluster workload network policy: %w", err)
	}
	return vclusterWorkloadNetworkPolicyMatches(workload, request.Identity.VClusterName, request.Runtime.Network), nil
}

func vclusterControlPlaneNetworkPolicyMatches(policy *networkingv1.NetworkPolicy, releaseName string) bool {
	if policy == nil || policy.Spec.PodSelector.MatchLabels["release"] != releaseName {
		return false
	}
	for _, rule := range policy.Spec.Ingress {
		for _, peer := range rule.From {
			if peer.PodSelector == nil || peer.PodSelector.MatchLabels[vk8sTerminalComponentLabel] != vk8sTerminalComponentValue {
				continue
			}
			for _, port := range rule.Ports {
				if port.Protocol != nil && *port.Protocol != corev1.ProtocolTCP {
					continue
				}
				if port.Port != nil && port.Port.IntValue() == 8443 {
					return true
				}
			}
		}
	}
	return false
}

func vclusterWorkloadNetworkPolicyMatches(policy *networkingv1.NetworkPolicy, releaseName string, network environment.VK8sNetwork) bool {
	if policy == nil || policy.Spec.PodSelector.MatchLabels["vcluster.loft.sh/managed-by"] != releaseName {
		return false
	}
	for _, rule := range policy.Spec.Egress {
		for _, peer := range rule.To {
			if peer.IPBlock == nil || peer.IPBlock.CIDR != network.PublicEgressCIDR || !sameStringSet(peer.IPBlock.Except, network.ProtectedCIDRs) {
				continue
			}
			return true
		}
	}
	return false
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	if len(values) != len(right) {
		return false
	}
	for _, value := range right {
		if _, ok := values[value]; !ok {
			return false
		}
	}
	return true
}

func (p *vk8sEnvironmentProvider) controlPlaneReady(ctx context.Context, identity environment.VK8sEnvironmentIdentity) (bool, error) {
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

func (p *vk8sEnvironmentProvider) readKubeconfig(ctx context.Context, identity environment.VK8sEnvironmentIdentity) ([]byte, error) {
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

func (p *vk8sEnvironmentProvider) vclusterServerAddress(ctx context.Context, identity environment.VK8sEnvironmentIdentity) (string, error) {
	_, err := p.k8s.Clientset().CoreV1().Services(identity.Namespace).Get(ctx, identity.VClusterName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return vclusterServiceAddress(identity), nil
}

func vclusterServiceAddress(identity environment.VK8sEnvironmentIdentity) string {
	// vcluster's serving certificate covers the service short name and namespace,
	// but not the fully-qualified .svc.cluster.local name.
	return fmt.Sprintf("https://%s.%s:443", identity.VClusterName, identity.Namespace)
}

func (p *vk8sEnvironmentProvider) upsertKubeconfig(ctx context.Context, request environment.VK8sProvisionRequest, config []byte) error {
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

func (p *vk8sEnvironmentProvider) ensureTerminal(ctx context.Context, request environment.VK8sProvisionRequest) error {
	pods := p.k8s.Clientset().CoreV1().Pods(request.Identity.Namespace)
	pod, err := pods.Get(ctx, request.Identity.TerminalPodName, metav1.GetOptions{})
	if err == nil {
		if pod.Annotations[vk8sEnvironmentUIDAnnotation] != request.EnvironmentUID || pod.Annotations[vk8sEnvironmentRevisionAnnotation] != request.Revision || pod.Labels[vk8sTerminalComponentLabel] != vk8sTerminalComponentValue || pod.Spec.ServiceAccountName != vk8sRuntimeServiceAccount || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Image != request.Runtime.ImageDigest {
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

func (p *vk8sEnvironmentProvider) ensureTerminalNetworkPolicy(ctx context.Context, request environment.VK8sProvisionRequest) error {
	desired, err := newVK8sTerminalNetworkPolicy(request)
	if err != nil {
		return err
	}
	policies := p.k8s.Clientset().NetworkingV1().NetworkPolicies(request.Identity.Namespace)
	existing, err := policies.Get(ctx, desired.Name, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		if _, createErr := policies.Create(ctx, desired, metav1.CreateOptions{}); createErr != nil && !k8serrors.IsAlreadyExists(createErr) {
			return fmt.Errorf("create VK8s terminal network policy: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get VK8s terminal network policy: %w", err)
	}
	if err := verifyVK8sTerminalNetworkPolicy(existing, request); err != nil {
		return err
	}
	if !apiequality.Semantic.DeepEqual(existing.Spec, desired.Spec) {
		return fmt.Errorf("VK8s terminal network policy differs from immutable runtime snapshot")
	}
	return nil
}

func newVK8sTerminalNetworkPolicy(request environment.VK8sProvisionRequest) (*networkingv1.NetworkPolicy, error) {
	if err := request.Runtime.Network.Validate(); err != nil {
		return nil, fmt.Errorf("validate VK8s terminal network policy: %w", err)
	}
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vk8sTerminalNetworkPolicyName,
			Namespace: request.Identity.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "breakfix",
				vk8sRuntimeLabel:            vk8sRuntimeLabelValue,
				vk8sTerminalComponentLabel:  vk8sTerminalComponentValue,
			},
			Annotations: map[string]string{
				vk8sEnvironmentUIDAnnotation:      request.EnvironmentUID,
				vk8sEnvironmentRevisionAnnotation: request.Revision,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{vk8sTerminalComponentLabel: vk8sTerminalComponentValue}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"release": request.Identity.VClusterName}}}},
					Ports: []networkingv1.NetworkPolicyPort{
						networkPolicyPort(corev1.ProtocolTCP, 443),
						networkPolicyPort(corev1.ProtocolTCP, 8443),
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}},
						PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
					}},
					Ports: []networkingv1.NetworkPolicyPort{
						networkPolicyPort(corev1.ProtocolUDP, 53),
						networkPolicyPort(corev1.ProtocolTCP, 53),
					},
				},
				{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
					CIDR: request.Runtime.Network.PublicEgressCIDR, Except: append([]string(nil), request.Runtime.Network.ProtectedCIDRs...),
				}}}},
			},
		},
	}, nil
}

func verifyVK8sTerminalNetworkPolicy(policy *networkingv1.NetworkPolicy, request environment.VK8sProvisionRequest) error {
	if policy == nil || policy.Namespace != request.Identity.Namespace || policy.Annotations[vk8sEnvironmentUIDAnnotation] != request.EnvironmentUID || policy.Annotations[vk8sEnvironmentRevisionAnnotation] != request.Revision || policy.Labels[vk8sTerminalComponentLabel] != vk8sTerminalComponentValue {
		return fmt.Errorf("VK8s terminal network policy has different ownership metadata")
	}
	return nil
}

func networkPolicyPort(protocol corev1.Protocol, port int) networkingv1.NetworkPolicyPort {
	protocolValue := protocol
	portValue := intstr.FromInt(port)
	return networkingv1.NetworkPolicyPort{Protocol: &protocolValue, Port: &portValue}
}

func newVK8sTerminalPod(request environment.VK8sProvisionRequest, resources corev1.ResourceRequirements, kubeconfigMode int32) *corev1.Pod {
	automount := false
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: request.Identity.TerminalPodName, Namespace: request.Identity.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/part-of": "breakfix",
				vk8sRuntimeLabel:            vk8sRuntimeLabelValue,
				vk8sTerminalComponentLabel:  vk8sTerminalComponentValue,
			},
			Annotations: map[string]string{vk8sEnvironmentUIDAnnotation: request.EnvironmentUID, vk8sEnvironmentRevisionAnnotation: request.Revision},
		},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &automount,
			RestartPolicy:                corev1.RestartPolicyNever,
			ServiceAccountName:           vk8sRuntimeServiceAccount,
			Containers: []corev1.Container{{
				Name: "scenario", Image: request.Runtime.ImageDigest, ImagePullPolicy: corev1.PullIfNotPresent,
				Resources:    resources,
				Env:          []corev1.EnvVar{{Name: "KUBECONFIG", Value: "/root/.kube/config"}},
				VolumeMounts: []corev1.VolumeMount{{Name: "kubeconfig", MountPath: "/root/.kube", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: "kubeconfig", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: request.Identity.KubeconfigSecretName, DefaultMode: &kubeconfigMode,
				Items: []corev1.KeyToPath{{Key: "config", Path: "config"}},
			}}}},
		},
	}
}

func (p *vk8sEnvironmentProvider) observeTerminal(ctx context.Context, request environment.VK8sProvisionRequest) (environment.InitializationObservation, error) {
	pod, err := p.k8s.Clientset().CoreV1().Pods(request.Identity.Namespace).Get(ctx, request.Identity.TerminalPodName, metav1.GetOptions{})
	if k8serrors.IsNotFound(err) {
		return environment.InitializationObservation{}, nil
	}
	if err != nil {
		return environment.InitializationObservation{}, fmt.Errorf("get VK8s terminal pod: %w", err)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "scenario" {
			continue
		}
		if status.State.Waiting != nil && terminalPodWaitingReason(status.State.Waiting.Reason) {
			return environment.InitializationObservation{}, fmt.Errorf("VK8s terminal cannot start: %s: %s", status.State.Waiting.Reason, strings.TrimSpace(status.State.Waiting.Message))
		}
		if status.State.Terminated != nil {
			return environment.InitializationObservation{Failed: true, ExitCode: int(status.State.Terminated.ExitCode), Message: terminalTerminationMessage(status.State.Terminated)}, nil
		}
		if !status.Ready {
			return environment.InitializationObservation{}, nil
		}
		result, err := p.k8s.ExecInPodStreamsContext(ctx, request.Identity.Namespace, request.Identity.TerminalPodName, 4096, "test", "-f", vk8sInitSentinel)
		if err != nil {
			return environment.InitializationObservation{}, err
		}
		if result.ExitCode == 0 {
			return environment.InitializationObservation{Complete: true}, nil
		}
		if result.ExitCode == 1 {
			return environment.InitializationObservation{}, nil
		}
		return environment.InitializationObservation{}, fmt.Errorf("inspect VK8s runtime initializer: exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return environment.InitializationObservation{}, nil
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
		return "scenario terminal terminated"
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

func vk8sWorkloadResources(snapshot environment.VK8sRuntimeResources) (corev1.ResourceRequirements, error) {
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

func writeVK8sValuesFile(runtime environment.VK8sRuntime) (string, error) {
	if err := runtime.Network.Validate(); err != nil {
		return "", fmt.Errorf("validate VK8s network policy: %w", err)
	}
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

func buildVK8sValues(runtime environment.VK8sRuntime) map[string]any {
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
	setNestedValue(values, true, "policies", "networkPolicy", "enabled")
	setNestedValue(values, []any{map[string]any{
		"from": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{
			vk8sTerminalComponentLabel: vk8sTerminalComponentValue,
		}}}},
		"ports": []any{map[string]any{"protocol": string(corev1.ProtocolTCP), "port": 8443}},
	}}, "policies", "networkPolicy", "controlPlane", "ingress")
	// Baseline rejects host-networked and privileged learner workloads. Without
	// it, a workload could bypass the host-side NetworkPolicy entirely.
	setNestedValue(values, "baseline", "policies", "podSecurityStandard")
	// Learners can describe virtual NetworkPolicies for their own workloads,
	// but must never be allowed to replace the host-side isolation boundary.
	setNestedValue(values, false, "sync", "toHost", "networkPolicies", "enabled")
	setNestedValue(values, true, "policies", "networkPolicy", "workload", "publicEgress", "enabled")
	setNestedValue(values, runtime.Network.PublicEgressCIDR, "policies", "networkPolicy", "workload", "publicEgress", "cidr")
	setNestedValue(values, append([]string(nil), runtime.Network.ProtectedCIDRs...), "policies", "networkPolicy", "workload", "publicEgress", "except")
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
