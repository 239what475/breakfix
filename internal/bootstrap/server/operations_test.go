package server

import (
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
)

func TestOperationsRuntimeConfigConvertsNodeAndK8sProfiles(t *testing.T) {
	cfg := config.Config{
		Incus: incus.Config{MaxNodesPerEnvironment: 4, BaseImageFingerprint: strings.Repeat("a", 64), NodeMemory: "512MiB", NodeRootDisk: "5GiB", NodeCPU: "1", NodeProcesses: 128},
		Runtime: config.RuntimeConfig{
			Node: config.NodeRuntimeConfig{ProfileRevision: "node-profile"},
			K8s:  config.K8sRuntimeConfig{BaseImageDigest: "registry.example/base@sha256:" + strings.Repeat("b", 64), ProfileRevision: "k8s-profile", Version: "v1.36", Resources: config.K8sResourceConfig{WorkloadCPU: "1", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "1Gi"}},
		},
	}
	value, err := operationsRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("operations runtime config: %v", err)
	}
	if value.MaxNodes != 4 || value.Node.ProfileRevision != "node-profile" || value.Node.Resources.MemoryBytes <= 0 || value.K8s.Resources.MemoryBytes <= 0 {
		t.Fatalf("converted Operations config = %#v", value)
	}
	if err := value.Lifecycle.Validate(); err != nil {
		t.Fatalf("converted lifecycle: %v", err)
	}
}

func TestOperationsRuntimeConfigRejectsInvalidResource(t *testing.T) {
	cfg := config.Config{Incus: incus.Config{MaxNodesPerEnvironment: 1, NodeMemory: "invalid", NodeRootDisk: "1GiB", NodeCPU: "1", NodeProcesses: 1}}
	if _, err := operationsRuntimeConfig(cfg); err == nil {
		t.Fatal("invalid Node memory was accepted")
	}
}

func TestDocumentationProfilesAreFixedToTheKubernetesPodLifecycleRuntime(t *testing.T) {
	cfg := config.Config{Runtime: config.RuntimeConfig{
		Node: config.NodeRuntimeConfig{ProfileRevision: "node-profile", NetworkPolicyRevision: "node-network"},
		K8s:  config.K8sRuntimeConfig{BaseImageDigest: "registry.example/base@sha256:" + strings.Repeat("b", 64), ManagementTerminalImage: "registry.example/base@sha256:" + strings.Repeat("b", 64), ProfileRevision: "k8s-profile", Version: "v1.36", Resources: config.K8sResourceConfig{ControlPlaneCPU: "1", ControlPlaneMemory: "1Gi", ControlPlaneEphemeralStorage: "1Gi", WorkloadCPU: "1", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "1Gi", QuotaCPU: "3", QuotaMemory: "3Gi", QuotaEphemeralStorage: "30Gi"}, Network: config.K8sNetworkConfig{PublicEgressCIDR: "0.0.0.0/0", ProtectedCIDRs: []string{"10.0.0.0/8"}}},
	}}
	profiles, err := newDocumentationProfiles(cfg)
	if err != nil {
		t.Fatal(err)
	}
	constraint := profiles.DocumentationRuntimeConstraints()[0]
	if constraint.Runtime != "k8s" || constraint.Network != "isolated" || profiles.DocumentationLifecyclePolicy().MaxLifetimeSeconds != documentationMaxLifetimeSeconds {
		t.Fatalf("documentation profile = %#v %#v", constraint, profiles.DocumentationLifecyclePolicy())
	}
	if _, err := profiles.ResolveDocumentationRuntimeProfile(constraint); err != nil {
		t.Fatal(err)
	}
	constraint.Topology = "expanded"
	if _, err := profiles.ResolveDocumentationRuntimeProfile(constraint); err == nil {
		t.Fatal("unapproved documentation runtime constraint was accepted")
	}
}
