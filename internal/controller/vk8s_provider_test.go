package controller

import (
	"testing"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestVClusterServiceAddressMatchesServingCertificateName(t *testing.T) {
	identity := VK8sEnvironmentIdentity{
		Namespace:    "breakfix-vk8s-environment",
		VClusterName: "vc-environment",
	}

	if got, want := vclusterServiceAddress(identity), "https://vc-environment.breakfix-vk8s-environment:443"; got != want {
		t.Fatalf("vcluster service address = %q, want %q", got, want)
	}
}

func TestNewVK8sTerminalPodUsesRuntimeServiceAccountForRegistryPull(t *testing.T) {
	request := VK8sProvisionRequest{
		EnvironmentUID: "environment-uid",
		Revision:       "revision",
		Identity: VK8sEnvironmentIdentity{
			Namespace: "breakfix-vk8s-environment", TerminalPodName: "terminal", KubeconfigSecretName: "kubeconfig",
		},
		Runtime: breakfixv1.VK8sRuntimeSnapshot{
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
}

func vk8sTestResources(t *testing.T) corev1.ResourceRequirements {
	t.Helper()
	resources, err := vk8sWorkloadResources(breakfixv1.VK8sResourceSnapshot{
		WorkloadCPU: "500m", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "1Gi",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resources
}
