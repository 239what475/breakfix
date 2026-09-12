package generation

import (
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/environment"
)

func TestVerificationReportRequiresExactAnswerCoverage(t *testing.T) {
	snapshot := validNodeExecutionSnapshot()
	valid := VerificationReport{
		Passed: true,
		Answers: []ExecutionResult{
			{Location: "client", ExitCode: 0},
			{Location: "server", ExitCode: 0},
		},
		Checkpoints: []CheckpointResult{{ID: "service-ready", Passed: true, Summary: "service is ready"}},
		Summary:     "all checks passed",
	}
	if err := valid.Validate(snapshot); err != nil {
		t.Fatalf("valid Node report: %v", err)
	}

	tests := []struct {
		name    string
		answers []ExecutionResult
		want    string
	}{
		{
			name:    "missing node",
			answers: []ExecutionResult{{Location: "client", ExitCode: 0}},
			want:    "does not cover every answer location",
		},
		{
			name: "duplicate node",
			answers: []ExecutionResult{
				{Location: "client", ExitCode: 0},
				{Location: "client", ExitCode: 0},
			},
			want: "duplicate answer location",
		},
		{
			name: "unknown node",
			answers: []ExecutionResult{
				{Location: "client", ExitCode: 0},
				{Location: "other", ExitCode: 0},
			},
			want: "unknown answer location",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := valid
			report.Answers = test.answers
			err := report.Validate(snapshot)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerificationReportRequiresManagementAnswerForK8s(t *testing.T) {
	snapshot := ExecutionSnapshot{
		Runtime:     scenario.RuntimeK8s,
		Checkpoints: []CheckpointSnapshot{{ID: "deployment-ready"}},
		K8s: &K8sRuntimeSnapshot{
			BaseImageDigest:         "registry.example.com/base@sha256:" + strings.Repeat("a", 64),
			ProfileRevision:         "k8s-profile-v1",
			Version:                 "v0.28.0",
			ManagementTerminalImage: "registry.example.com/terminal@sha256:" + strings.Repeat("b", 64),
			Resources: K8sResources{
				ControlPlaneCPU: "1", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "1Gi",
				WorkloadCPU: "1", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "1Gi",
				QuotaCPU: "3", QuotaMemory: "3Gi", QuotaEphemeralStorage: "30Gi",
			},
			Network: environment.VK8sNetwork{
				PublicEgressCIDR: "0.0.0.0/0",
				ProtectedCIDRs:   []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
			},
		},
	}
	report := VerificationReport{
		Passed:      true,
		Answers:     []ExecutionResult{{Location: "management", ExitCode: 0}},
		Checkpoints: []CheckpointResult{{ID: "deployment-ready", Passed: true, Summary: "deployment is ready"}},
		Summary:     "all checks passed",
	}
	if err := report.Validate(snapshot); err != nil {
		t.Fatalf("valid K8s report: %v", err)
	}
	report.Answers = []ExecutionResult{{Location: "worker", ExitCode: 0}}
	if err := report.Validate(snapshot); err == nil || !strings.Contains(err.Error(), "unknown answer location") {
		t.Fatalf("K8s report error = %v", err)
	}
}

func validNodeExecutionSnapshot() ExecutionSnapshot {
	return ExecutionSnapshot{
		Runtime:     scenario.RuntimeNode,
		Checkpoints: []CheckpointSnapshot{{ID: "service-ready", Node: "client"}},
		Node: &NodeRuntimeSnapshot{
			BaseImageFingerprint:  strings.Repeat("a", 64),
			ProfileRevision:       "node-profile-v1",
			NetworkPolicyRevision: "network-v1",
			Nodes: []NodeSnapshot{
				{Name: "client", Title: "Client"},
				{Name: "server", Title: "Server"},
			},
			Resources: NodeResources{CPU: "1", Memory: "512MiB", Processes: 512, RootDisk: "5GiB"},
		},
	}
}
