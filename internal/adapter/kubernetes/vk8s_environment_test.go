package kubernetes

import (
	"testing"

	"github.com/breakfix/breakfix/internal/domain/environment"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVClusterServiceAddressMatchesServingCertificateName(t *testing.T) {
	identity := environment.VK8sEnvironmentIdentity{
		Namespace:    "breakfix-vk8s-environment",
		VClusterName: "vc-environment",
	}

	if got, want := vclusterServiceAddress(identity), "https://vc-environment.breakfix-vk8s-environment:443"; got != want {
		t.Fatalf("vcluster service address = %q, want %q", got, want)
	}
}

func TestVClusterNamespaceOwnershipAcceptsChartMetadataAfterReset(t *testing.T) {
	request := environment.VK8sProvisionRequest{EnvironmentUID: "environment-uid", Revision: "revision", Identity: environment.VK8sEnvironmentIdentity{Namespace: "breakfix-vk8s-environment"}}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: request.Identity.Namespace, Annotations: map[string]string{"vcluster.loft.sh/created": "true"}}}
	if verifyVK8sNamespaceOwner(namespace, request) == nil {
		t.Fatal("chart-owned namespace unexpectedly passed strict owner validation")
	}
	if !vclusterNamespaceOwnershipCompatible(namespace, request) {
		t.Fatal("chart-owned namespace was not accepted for deterministic reset recovery")
	}
	namespace.Annotations[vk8sEnvironmentUIDAnnotation] = "another-environment"
	if vclusterNamespaceOwnershipCompatible(namespace, request) {
		t.Fatal("namespace with a conflicting Breakfix owner was adopted")
	}
}

// The reset generation fence decides whether a retried reset wipes or adopts:
// a namespace stamped with the request's own generation is the rebuild that
// reset created, while any other state — unstamped, older generation, or a
// zero (provision/release) request — must delete.
func TestNamespaceResetGenerationFenceAdoptsOnlyTheCurrentRebuild(t *testing.T) {
	reset := environment.VK8sProvisionRequest{EnvironmentUID: "environment-uid", Revision: "revision", ResetNonce: 3, Identity: environment.VK8sEnvironmentIdentity{Namespace: "breakfix-vk8s-environment"}}
	stamped := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: reset.Identity.Namespace, Annotations: map[string]string{vk8sResetGenerationAnnotation: "3"}}}
	if !namespaceCarriesResetGeneration(stamped, reset) {
		t.Fatal("the rebuild of this reset generation was not adopted")
	}
	older := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: reset.Identity.Namespace, Annotations: map[string]string{vk8sResetGenerationAnnotation: "2"}}}
	if namespaceCarriesResetGeneration(older, reset) {
		t.Fatal("an older generation's namespace was adopted as the rebuild")
	}
	unstamped := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: reset.Identity.Namespace}}
	if namespaceCarriesResetGeneration(unstamped, reset) {
		t.Fatal("an unstamped namespace was adopted as the rebuild")
	}
	plain := reset
	plain.ResetNonce = 0
	if namespaceCarriesResetGeneration(stamped, plain) {
		t.Fatal("a provision or release request adopted the reset rebuild")
	}
}

func TestVClusterNamespaceOwnershipAcceptsBlankReleaseWithoutDigestFence(t *testing.T) {
	// A blank environment provisioned before a controller upgrade freezes the
	// plan digest into the namespace; release carries no digest and must clear
	// ownership on the environment UID alone.
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "breakfix-vk8s-environment", Labels: map[string]string{vk8sRuntimeLabel: vk8sRuntimeLabelValue}, Annotations: map[string]string{
		vk8sEnvironmentUIDAnnotation:      "environment-uid",
		vk8sEnvironmentRevisionAnnotation: "sha256:stale-plan-digest",
	}}}
	release := environment.VK8sProvisionRequest{EnvironmentUID: "environment-uid", Blank: true, Identity: environment.VK8sEnvironmentIdentity{Namespace: namespace.Name}}
	if err := verifyVK8sNamespaceOwner(namespace, release); err != nil {
		t.Fatalf("blank release was blocked by the digest fence: %v", err)
	}
	release.EnvironmentUID = "another-environment"
	if err := verifyVK8sNamespaceOwner(namespace, release); err == nil {
		t.Fatal("blank release adopted a namespace owned by another environment")
	}
	// Provision and reset still fence on the digest, blank or not.
	reset := environment.VK8sProvisionRequest{EnvironmentUID: "environment-uid", Revision: "sha256:another-plan-digest", Blank: true, Identity: release.Identity}
	if err := verifyVK8sNamespaceOwner(namespace, reset); err == nil {
		t.Fatal("blank reset accepted a namespace bound to another plan digest")
	}
	reset.Revision = "sha256:stale-plan-digest"
	if err := verifyVK8sNamespaceOwner(namespace, reset); err != nil {
		t.Fatalf("blank reset with the matching digest was rejected: %v", err)
	}
}

func TestNewVK8sTerminalPodUsesRuntimeServiceAccountForRegistryPull(t *testing.T) {
	request := environment.VK8sProvisionRequest{
		EnvironmentUID: "environment-uid",
		Revision:       "revision",
		Identity: environment.VK8sEnvironmentIdentity{
			Namespace: "breakfix-vk8s-environment", TerminalPodName: "terminal", KubeconfigSecretName: "kubeconfig",
		},
		Runtime: environment.VK8sRuntime{
			ImageDigest: "registry.example/breakfix/candidates/example@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	pod := newVK8sTerminalPod(request, vk8sTestResources(t), 0o600)

	if pod.Spec.ServiceAccountName != vk8sRuntimeServiceAccount {
		t.Fatalf("service account = %q, want %q", pod.Spec.ServiceAccountName, vk8sRuntimeServiceAccount)
	}
	if len(pod.Spec.ImagePullSecrets) != 0 {
		t.Fatalf("terminal Pod must inherit pull credentials from its ServiceAccount, got %#v", pod.Spec.ImagePullSecrets)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatalf("automount service account token = %#v, want false", pod.Spec.AutomountServiceAccountToken)
	}
	if pod.Labels[vk8sTerminalComponentLabel] != vk8sTerminalComponentValue {
		t.Fatalf("terminal component label = %q", pod.Labels[vk8sTerminalComponentLabel])
	}
	if len(pod.Spec.Containers[0].Env) != 1 || pod.Spec.Containers[0].Env[0].Name != "KUBECONFIG" {
		t.Fatalf("terminal environment = %#v, want only kubeconfig", pod.Spec.Containers[0].Env)
	}
}

func TestBuildVK8sValuesEnablesNativeNetworkPolicies(t *testing.T) {
	runtime := environment.VK8sRuntime{
		Version: "v1.36.2",
		Resources: environment.VK8sRuntimeResources{
			ControlPlaneCPU: "500m", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "1Gi",
			WorkloadCPU: "250m", WorkloadMemory: "256Mi", WorkloadEphemeralStorage: "1Gi",
			QuotaCPU: "2", QuotaMemory: "2Gi", QuotaEphemeralStorage: "4Gi",
		},
		Network: vk8sTestNetwork(),
	}
	values := buildVK8sValues(runtime)
	policy := values["policies"].(map[string]any)["networkPolicy"].(map[string]any)
	if policy["enabled"] != true {
		t.Fatalf("network policy enabled = %#v", policy["enabled"])
	}
	controlPlaneIngress := policy["controlPlane"].(map[string]any)["ingress"].([]any)
	if len(controlPlaneIngress) != 1 {
		t.Fatalf("control plane ingress = %#v", controlPlaneIngress)
	}
	controlPlaneRule := controlPlaneIngress[0].(map[string]any)
	controlPlanePeer := controlPlaneRule["from"].([]any)[0].(map[string]any)["podSelector"].(map[string]any)["matchLabels"].(map[string]string)
	if controlPlanePeer[vk8sTerminalComponentLabel] != vk8sTerminalComponentValue {
		t.Fatalf("control plane terminal selector = %#v", controlPlanePeer)
	}
	if got := values["policies"].(map[string]any)["podSecurityStandard"]; got != "baseline" {
		t.Fatalf("pod security standard = %#v, want baseline", got)
	}
	if got := values["sync"].(map[string]any)["toHost"].(map[string]any)["networkPolicies"].(map[string]any)["enabled"]; got != false {
		t.Fatalf("network policy sync enabled = %#v, want false", got)
	}
	public := policy["workload"].(map[string]any)["publicEgress"].(map[string]any)
	if public["enabled"] != true || public["cidr"] != "0.0.0.0/0" {
		t.Fatalf("workload public egress = %#v", public)
	}
	if !sameStringSet(public["except"].([]string), runtime.Network.ProtectedCIDRs) {
		t.Fatalf("workload public egress exceptions = %#v", public["except"])
	}
}

func TestNewVK8sTerminalNetworkPolicyRestrictsEgress(t *testing.T) {
	request := environment.VK8sProvisionRequest{
		EnvironmentUID: "environment-uid", Revision: "revision",
		Identity: environment.VK8sEnvironmentIdentity{
			Namespace: "breakfix-vk8s-environment", VClusterName: "vc-environment", TerminalPodName: "terminal",
		},
		Runtime: environment.VK8sRuntime{Network: vk8sTestNetwork()},
	}
	policy, err := newVK8sTerminalNetworkPolicy(request)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Name != vk8sTerminalNetworkPolicyName || policy.Spec.PodSelector.MatchLabels[vk8sTerminalComponentLabel] != vk8sTerminalComponentValue {
		t.Fatalf("terminal policy identity = %#v", policy)
	}
	if len(policy.Spec.Ingress) != 0 || len(policy.Spec.Egress) != 3 {
		t.Fatalf("terminal policy rules = %#v", policy.Spec)
	}
	controlPlane := policy.Spec.Egress[0]
	if len(controlPlane.To) != 1 || controlPlane.To[0].PodSelector == nil || controlPlane.To[0].PodSelector.MatchLabels["release"] != request.Identity.VClusterName {
		t.Fatalf("control plane rule = %#v", controlPlane)
	}
	dns := policy.Spec.Egress[1]
	if len(dns.To) != 1 || dns.To[0].NamespaceSelector == nil || dns.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
		t.Fatalf("DNS rule = %#v", dns)
	}
	public := policy.Spec.Egress[2]
	if len(public.To) != 1 || public.To[0].IPBlock == nil || public.To[0].IPBlock.CIDR != request.Runtime.Network.PublicEgressCIDR || !sameStringSet(public.To[0].IPBlock.Except, request.Runtime.Network.ProtectedCIDRs) {
		t.Fatalf("public egress rule = %#v", public)
	}
}

func TestVclusterWorkloadNetworkPolicyMatchesConfiguredBoundary(t *testing.T) {
	network := vk8sTestNetwork()
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-work-vc-environment"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"vcluster.loft.sh/managed-by": "vc-environment"}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{
				CIDR: network.PublicEgressCIDR, Except: append([]string(nil), network.ProtectedCIDRs...),
			}}}}},
		},
	}
	if !vclusterWorkloadNetworkPolicyMatches(policy, "vc-environment", network) {
		t.Fatal("expected native vcluster workload policy to match configured boundary")
	}
}

func TestVclusterControlPlaneNetworkPolicyAllowsManagementTerminal(t *testing.T) {
	policy := &networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"release": "vc-environment"}},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
					vk8sTerminalComponentLabel: vk8sTerminalComponentValue,
				}}}},
				Ports: []networkingv1.NetworkPolicyPort{networkPolicyPort(corev1.ProtocolTCP, 8443)},
			}},
		},
	}
	if !vclusterControlPlaneNetworkPolicyMatches(policy, "vc-environment") {
		t.Fatal("expected native vcluster control-plane policy to allow the management terminal")
	}
}

func vk8sTestNetwork() environment.VK8sNetwork {
	return environment.VK8sNetwork{
		PublicEgressCIDR: "0.0.0.0/0",
		ProtectedCIDRs:   []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
	}
}

func vk8sTestResources(t *testing.T) corev1.ResourceRequirements {
	t.Helper()
	resources, err := vk8sWorkloadResources(environment.VK8sRuntimeResources{
		WorkloadCPU: "500m", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "1Gi",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resources
}
