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
