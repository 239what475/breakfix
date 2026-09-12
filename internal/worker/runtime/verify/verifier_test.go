package verify

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/content/scenario"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestVerifyNodeRunsReproductionBeforeReferenceAnswer(t *testing.T) {
	node := &verificationNodeExecutor{outputs: map[string]incus.ExecNodeResult{
		"reproduce.sh": {ExitCode: 0, Stdout: `{"evidence":[{"id":"service-unavailable","observed":true,"summary":"service is unavailable"}]}`},
		"answer.sh":    {ExitCode: 0},
		"checks.sh":    {ExitCode: 0, Stdout: `{"checks":[{"id":"service-ready","passed":true,"summary":"service is ready"}]}`},
	}}
	executor := &Executor{node: node}
	report, err := executor.verifyNode(context.Background(), domainexecution.Work{ArchiveSHA256: "sha256:" + strings.Repeat("a", 64), Snapshot: verificationNodeSnapshot()}, verificationNodeEnvironment())
	if err != nil {
		t.Fatalf("verify node: %v", err)
	}
	if !report.Passed || len(report.Reproduction) != 1 || !report.Reproduction[0].Observed {
		t.Fatalf("verification report = %#v", report)
	}
	if got, want := strings.Join(node.scripts, ","), "reproduce.sh,answer.sh,checks.sh"; got != want {
		t.Fatalf("script order = %q, want %q", got, want)
	}
}

func TestVerifyNodeDoesNotRunReferenceAnswerWhenPhenomenonIsAbsent(t *testing.T) {
	node := &verificationNodeExecutor{outputs: map[string]incus.ExecNodeResult{
		"reproduce.sh": {ExitCode: 0, Stdout: `{"evidence":[{"id":"service-unavailable","observed":false,"summary":"service is already ready"}]}`},
	}}
	executor := &Executor{node: node}
	report, err := executor.verifyNode(context.Background(), domainexecution.Work{ArchiveSHA256: "sha256:" + strings.Repeat("a", 64), Snapshot: verificationNodeSnapshot()}, verificationNodeEnvironment())
	var artifact *domainexecution.ArtifactError
	if !errors.As(err, &artifact) || artifact.Code != "REPRODUCTION_FAILED" {
		t.Fatalf("verification error = %v, want reproduction artifact failure", err)
	}
	if report.Passed || len(report.Answers) != 0 || len(report.Checkpoints) != 0 || len(report.Reproduction) != 1 {
		t.Fatalf("reproduction failure report = %#v", report)
	}
	if got, want := strings.Join(node.scripts, ","), "reproduce.sh"; got != want {
		t.Fatalf("scripts after unreproduced evidence = %q, want %q", got, want)
	}
}

func TestVerifyNodeReturnsReproductionProtocolReport(t *testing.T) {
	node := &verificationNodeExecutor{outputs: map[string]incus.ExecNodeResult{
		"reproduce.sh": {ExitCode: 0, Stdout: "not JSON"},
	}}
	executor := &Executor{node: node}
	report, err := executor.verifyNode(context.Background(), domainexecution.Work{ArchiveSHA256: "sha256:" + strings.Repeat("a", 64), Snapshot: verificationNodeSnapshot()}, verificationNodeEnvironment())
	var artifact *domainexecution.ArtifactError
	if !errors.As(err, &artifact) || artifact.Code != "REPRODUCTION_PROTOCOL_FAILED" {
		t.Fatalf("verification error = %v, want reproduction protocol artifact failure", err)
	}
	if report.Passed || len(report.Reproduction) != 1 || report.Reproduction[0].Observed || report.Summary == "" {
		t.Fatalf("reproduction protocol report = %#v", report)
	}
	if artifact.Report == nil || artifact.Report.Summary != report.Summary {
		t.Fatalf("artifact report = %#v, want returned report", artifact.Report)
	}
}

func TestReproduceK8sParsesInitialEvidence(t *testing.T) {
	environments := &verificationEnvironmentClient{outputs: map[string]kubernetes.PodExecResult{
		"reproduce.sh": {ExitCode: 0, Stdout: `{"evidence":[{"id":"deployment-absent","observed":true,"summary":"deployment absent"}]}`},
	}}
	executor := &Executor{environments: environments}
	work := domainexecution.Work{Snapshot: domainexecution.Snapshot{
		Runtime:      scenario.RuntimeK8s,
		Reproduction: []domainexecution.ReproductionEvidenceSnapshot{{ID: "deployment-absent"}},
	}}
	evidence, err := executor.reproduceK8s(context.Background(), work, "verify", "terminal")
	if err != nil {
		t.Fatalf("reproduce K8s: %v", err)
	}
	if len(evidence) != 1 || !evidence[0].Observed || evidence[0].ID != "deployment-absent" {
		t.Fatalf("K8s reproduction evidence = %#v", evidence)
	}
	if got, want := strings.Join(environments.scripts, ","), "reproduce.sh"; got != want {
		t.Fatalf("K8s scripts = %q, want %q", got, want)
	}
}

func verificationNodeSnapshot() domainexecution.Snapshot {
	return domainexecution.Snapshot{
		Runtime:      scenario.RuntimeNode,
		Reproduction: []domainexecution.ReproductionEvidenceSnapshot{{ID: "service-unavailable", Node: "host"}},
		Checkpoints:  []domainexecution.CheckpointSnapshot{{ID: "service-ready", Node: "host"}},
		Node: &domainexecution.NodeRuntimeSnapshot{
			BaseImageFingerprint: strings.Repeat("a", 64), ProfileRevision: "profile", NetworkPolicyRevision: "network",
			Nodes:     []domainexecution.NodeSnapshot{{Name: "host", Title: "Host"}},
			Resources: domainexecution.NodeResources{CPU: "1", Memory: "512MiB", Processes: 64, RootDisk: "5GiB"},
		},
	}
}

func verificationNodeEnvironment() environmentRef {
	return environmentRef{
		runtime: scenario.RuntimeNode,
		uid:     types.UID("verify-node"),
		node: &breakfixv1.NodeEnvironment{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("verify-node")},
			Status:     breakfixv1.NodeEnvironmentStatus{Runtime: breakfixv1.NodeRuntimeStatus{Project: "project", Network: "network", ACL: "acl", Profile: "profile"}},
		},
	}
}

type verificationNodeExecutor struct {
	outputs map[string]incus.ExecNodeResult
	scripts []string
}

func (e *verificationNodeExecutor) ExecNode(_ context.Context, request incus.ExecNodeRequest) (incus.ExecNodeResult, error) {
	script := path.Base(request.Command[len(request.Command)-1])
	e.scripts = append(e.scripts, script)
	return e.outputs[script], nil
}

type verificationEnvironmentClient struct {
	outputs map[string]kubernetes.PodExecResult
	scripts []string
}

func (e *verificationEnvironmentClient) CreateNodeEnvironment(context.Context, string, *breakfixv1.NodeEnvironment) (*breakfixv1.NodeEnvironment, error) {
	return nil, errors.New("unexpected CreateNodeEnvironment")
}

func (e *verificationEnvironmentClient) GetNodeEnvironment(context.Context, string, string) (*breakfixv1.NodeEnvironment, error) {
	return nil, errors.New("unexpected GetNodeEnvironment")
}

func (e *verificationEnvironmentClient) DeleteNodeEnvironmentWithUID(context.Context, string, string, types.UID) error {
	return errors.New("unexpected DeleteNodeEnvironmentWithUID")
}

func (e *verificationEnvironmentClient) CreateVK8sEnvironment(context.Context, string, *breakfixv1.VK8sEnvironment) (*breakfixv1.VK8sEnvironment, error) {
	return nil, errors.New("unexpected CreateVK8sEnvironment")
}

func (e *verificationEnvironmentClient) GetVK8sEnvironment(context.Context, string, string) (*breakfixv1.VK8sEnvironment, error) {
	return nil, errors.New("unexpected GetVK8sEnvironment")
}

func (e *verificationEnvironmentClient) DeleteVK8sEnvironmentWithUID(context.Context, string, string, types.UID) error {
	return errors.New("unexpected DeleteVK8sEnvironmentWithUID")
}

func (e *verificationEnvironmentClient) ExecInPodStreamsContext(_ context.Context, _ string, _ string, _ int, command ...string) (kubernetes.PodExecResult, error) {
	script := path.Base(command[len(command)-1])
	e.scripts = append(e.scripts, script)
	return e.outputs[script], nil
}
