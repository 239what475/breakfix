package controller

import (
	"strings"
	"testing"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func TestVClusterReleaseNameRespectsHelmLimit(t *testing.T) {
	first := vclusterReleaseName(strings.Repeat("verify-env-", 12))
	second := vclusterReleaseName(strings.Repeat("verify-env-", 11) + "other")
	if len(first) > maxVClusterReleaseNameLength {
		t.Fatalf("vclusterReleaseName() length = %d, want <= %d: %q", len(first), maxVClusterReleaseNameLength, first)
	}
	if first == second {
		t.Fatalf("vclusterReleaseName() collided: %q", first)
	}
}

func TestRewriteVClusterKubeconfigTargetsVirtualClusterWithoutHostNamespace(t *testing.T) {
	raw := []byte(`apiVersion: v1
clusters:
- cluster:
    server: https://localhost:8443
  name: vcluster
contexts:
- context:
    cluster: vcluster
    namespace: breakfix-u-author-environment
    user: vcluster
  name: vcluster
current-context: vcluster
kind: Config
users:
- name: vcluster
  user:
    token: token
`)
	rewritten, err := rewriteVClusterKubeconfig(raw, "https://10.96.0.10:443")
	if err != nil {
		t.Fatalf("rewriteVClusterKubeconfig: %v", err)
	}
	cfg, err := clientcmd.Load(rewritten)
	if err != nil {
		t.Fatalf("load rewritten kubeconfig: %v", err)
	}
	if got := cfg.Clusters["vcluster"].Server; got != "https://10.96.0.10:443" {
		t.Fatalf("cluster server = %q, want rewritten service address", got)
	}
	if got := cfg.Contexts["vcluster"].Namespace; got != "default" {
		t.Fatalf("context namespace = %q, want virtual cluster default namespace", got)
	}
}

func TestResolveVClusterRuntimeAppliesProfileAndOverrides(t *testing.T) {
	runtime, err := resolveVClusterRuntime(breakfixv1.VClusterRuntimeSpec{
		Profile:            "default-k8s",
		Version:            "v1.31.2",
		ControlPlaneCPU:    "300m",
		ControlPlaneMemory: "640Mi",
		QuotaCPU:           "6",
	})
	if err != nil {
		t.Fatalf("resolveVClusterRuntime: %v", err)
	}
	if runtime.Profile != "default-k8s" {
		t.Fatalf("expected profile default-k8s, got %q", runtime.Profile)
	}
	if runtime.Version != "v1.31.2" {
		t.Fatalf("expected version v1.31.2, got %q", runtime.Version)
	}
	if runtime.ControlPlaneCPU != "300m" {
		t.Fatalf("expected overridden control plane cpu, got %q", runtime.ControlPlaneCPU)
	}
	if runtime.ControlPlaneMemory != "640Mi" {
		t.Fatalf("expected overridden control plane memory, got %q", runtime.ControlPlaneMemory)
	}
	if runtime.QuotaCPU != "6" {
		t.Fatalf("expected overridden quota cpu, got %q", runtime.QuotaCPU)
	}
	if runtime.QuotaMemory != "4Gi" {
		t.Fatalf("expected profile default quota memory, got %q", runtime.QuotaMemory)
	}
}

func TestResolveVClusterRuntimeRejectsUnknownProfile(t *testing.T) {
	if _, err := resolveVClusterRuntime(breakfixv1.VClusterRuntimeSpec{Profile: "nope"}); err == nil {
		t.Fatal("expected unknown vcluster profile to fail")
	}
}

func TestResolveVClusterRuntimeRejectsInvalidQuantities(t *testing.T) {
	if _, err := resolveVClusterRuntime(breakfixv1.VClusterRuntimeSpec{ControlPlaneCPU: "not-a-quantity"}); err == nil {
		t.Fatal("expected invalid quantity to fail")
	}
}

func TestBuildVClusterValuesIncludesRequestedSettings(t *testing.T) {
	values := buildVClusterValues(effectiveVClusterRuntime{
		Version:               "v1.31.2",
		ControlPlaneCPU:       "250m",
		ControlPlaneMemory:    "512Mi",
		QuotaCPU:              "4",
		QuotaMemory:           "4Gi",
		QuotaEphemeralStorage: "12Gi",
	})

	if got := nestedString(values, "controlPlane", "distro", "k8s", "version"); got != "v1.31.2" {
		t.Fatalf("expected distro version to be set, got %q", got)
	}
	if got := nestedBool(values, "controlPlane", "distro", "k8s", "enabled"); !got {
		t.Fatal("expected distro to be enabled when version is set")
	}
	if got := nestedString(values, "controlPlane", "statefulSet", "resources", "requests", "cpu"); got != "250m" {
		t.Fatalf("expected control plane cpu request, got %q", got)
	}
	if got := nestedString(values, "policies", "resourceQuota", "quota", "limits.memory"); got != "4Gi" {
		t.Fatalf("expected quota memory limit, got %q", got)
	}
	if got := nestedString(values, "policies", "resourceQuota", "quota", "requests.ephemeral-storage"); got != "12Gi" {
		t.Fatalf("expected quota ephemeral storage request, got %q", got)
	}
}

func nestedString(root map[string]any, path ...string) string {
	current := root
	for i, key := range path {
		if i == len(path)-1 {
			if val, ok := current[key].(string); ok {
				return val
			}
			return ""
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

func nestedBool(root map[string]any, path ...string) bool {
	current := root
	for i, key := range path {
		if i == len(path)-1 {
			val, _ := current[key].(bool)
			return val
		}
		next, ok := current[key].(map[string]any)
		if !ok {
			return false
		}
		current = next
	}
	return false
}
