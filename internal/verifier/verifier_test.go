package verifier

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/candidateworker"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/verification"
	"github.com/breakfix/breakfix/internal/worklist"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func TestExecuteNodeVerificationRunsAnswerChecksAndCleansEnvironment(t *testing.T) {
	environments := &verificationEnvironmentClient{}
	reporter := &verificationReporter{}
	node := &verificationNodeExecutor{checks: `{"checks":[{"id":"service-ready","passed":true,"summary":"service is ready"}]}`}
	executor, err := NewExecutor(reporter, environments, node, "breakfix-system")
	if err != nil {
		t.Fatal(err)
	}

	claim := nodeVerificationClaim()
	if err := executor.Execute(context.Background(), claim); err != nil {
		t.Fatalf("execute verification: %v", err)
	}
	if environments.created == nil {
		t.Fatal("verification environment was not created")
	}
	if environments.created.Spec.Environment.Purpose != breakfixv1.EnvironmentPurposeVerification {
		t.Fatalf("environment purpose = %q", environments.created.Spec.Environment.Purpose)
	}
	if environments.created.Name != verification.EnvironmentName(claim.Work.Item.ID) {
		t.Fatalf("environment name = %q", environments.created.Name)
	}
	if reporter.recorded == nil || reporter.recorded.Name != environments.created.Name || reporter.recorded.UID != string(verificationEnvironmentUID) {
		t.Fatalf("recorded environment = %#v", reporter.recorded)
	}
	if reporter.completed == nil || !reporter.completed.Passed || len(reporter.completed.Checkpoints) != 1 || !reporter.completed.Checkpoints[0].Passed {
		t.Fatalf("completed report = %#v", reporter.completed)
	}
	if len(environments.deleted) != 1 || environments.deleted[0] != verificationEnvironmentUID {
		t.Fatalf("deleted environment UIDs = %#v", environments.deleted)
	}
	if got, want := node.commands, []string{"answer.sh", "checks.sh"}; !sameStrings(got, want) {
		t.Fatalf("node commands = %#v, want %#v", got, want)
	}
}

func TestExecuteNodeVerificationClassifiesFailedCheckpointAsArtifact(t *testing.T) {
	environments := &verificationEnvironmentClient{}
	reporter := &verificationReporter{}
	node := &verificationNodeExecutor{checks: `{"checks":[{"id":"service-ready","passed":false,"summary":"service is down"}]}`}
	executor, err := NewExecutor(reporter, environments, node, "breakfix-system")
	if err != nil {
		t.Fatal(err)
	}

	err = executor.Execute(context.Background(), nodeVerificationClaim())
	var artifact *candidateworker.ArtifactError
	if !errors.As(err, &artifact) {
		t.Fatalf("verification error = %v, want artifact failure", err)
	}
	if artifact.Failure.Code != "CHECKPOINTS_FAILED" || artifact.Report == nil || artifact.Report.Passed {
		t.Fatalf("artifact failure = %#v", artifact)
	}
	if reporter.completed != nil {
		t.Fatalf("failed verification unexpectedly completed: %#v", reporter.completed)
	}
	if len(environments.deleted) != 1 || environments.deleted[0] != verificationEnvironmentUID {
		t.Fatalf("failed verification did not clean its environment: %#v", environments.deleted)
	}
}

func TestDeleteAndWaitTreatsAlreadyDeletedEnvironmentAsClean(t *testing.T) {
	environments := &verificationEnvironmentClient{deleteErr: apierrors.NewNotFound(nodeEnvironmentResource, "verify-env")}
	executor := &Executor{environments: environments, namespace: "breakfix-system"}
	if err := executor.deleteAndWait(context.Background(), environmentRef{runtime: challenge.RuntimeNode, name: "verify-env", uid: verificationEnvironmentUID}); err != nil {
		t.Fatalf("cleanup already-deleted environment: %v", err)
	}
}

const verificationEnvironmentUID types.UID = "verify-environment-uid"

var nodeEnvironmentResource = schema.GroupResource{Group: "breakfix.dev", Resource: "nodeenvironments"}

type verificationReporter struct {
	recorded  *candidate.VerificationEnvironment
	completed *candidate.VerificationReport
}

func (r *verificationReporter) RecordVerificationEnvironment(_ context.Context, _ candidateworker.Claim, environment candidate.VerificationEnvironment) error {
	copy := environment
	r.recorded = &copy
	return nil
}

func (r *verificationReporter) CompleteVerification(_ context.Context, _ candidateworker.Claim, report candidate.VerificationReport) error {
	copy := report
	r.completed = &copy
	return nil
}

type verificationEnvironmentClient struct {
	created   *breakfixv1.NodeEnvironment
	active    *breakfixv1.NodeEnvironment
	deleted   []types.UID
	deleteErr error
}

func (c *verificationEnvironmentClient) CreateNodeEnvironment(_ context.Context, _ string, environment *breakfixv1.NodeEnvironment) (*breakfixv1.NodeEnvironment, error) {
	if c.created != nil {
		return nil, fmt.Errorf("verification environment was created twice")
	}
	created := environment.DeepCopy()
	created.UID = verificationEnvironmentUID
	created.Status.Environment.Phase = breakfixv1.EnvironmentReady
	created.Status.Runtime = breakfixv1.NodeRuntimeStatus{
		Project: "project", Network: "network", ACL: "acl", Profile: "profile",
		Nodes: []breakfixv1.NodeInstanceStatus{{Name: "host", InstanceName: "host-instance", Address: "10.0.0.10"}},
	}
	c.created = created
	c.active = created.DeepCopy()
	return created.DeepCopy(), nil
}

func (c *verificationEnvironmentClient) GetNodeEnvironment(_ context.Context, _ string, _ string) (*breakfixv1.NodeEnvironment, error) {
	if c.active == nil {
		return nil, apierrors.NewNotFound(nodeEnvironmentResource, "verify-env")
	}
	return c.active.DeepCopy(), nil
}

func (c *verificationEnvironmentClient) DeleteNodeEnvironmentWithUID(_ context.Context, _ string, _ string, uid types.UID) error {
	c.deleted = append(c.deleted, uid)
	if c.deleteErr != nil {
		return c.deleteErr
	}
	if c.active == nil {
		return apierrors.NewNotFound(nodeEnvironmentResource, "verify-env")
	}
	if c.active.UID != uid {
		return apierrors.NewConflict(nodeEnvironmentResource, c.active.Name, errors.New("UID precondition failed"))
	}
	c.active = nil
	return nil
}

func (*verificationEnvironmentClient) CreateVK8sEnvironment(context.Context, string, *breakfixv1.VK8sEnvironment) (*breakfixv1.VK8sEnvironment, error) {
	return nil, errors.New("unexpected VK8s environment")
}

func (*verificationEnvironmentClient) GetVK8sEnvironment(context.Context, string, string) (*breakfixv1.VK8sEnvironment, error) {
	return nil, errors.New("unexpected VK8s environment")
}

func (*verificationEnvironmentClient) DeleteVK8sEnvironmentWithUID(context.Context, string, string, types.UID) error {
	return errors.New("unexpected VK8s environment")
}

func (*verificationEnvironmentClient) ExecInPodStreamsContext(context.Context, string, string, int, ...string) (k8s.PodExecResult, error) {
	return k8s.PodExecResult{}, errors.New("unexpected Kubernetes exec")
}

type verificationNodeExecutor struct {
	checks   string
	commands []string
}

func (e *verificationNodeExecutor) ExecNode(_ context.Context, request incusprovider.ExecNodeRequest) (incusprovider.ExecNodeResult, error) {
	if len(request.Command) != 2 || request.Command[0] != "/bin/bash" || request.LogicalName != "host" {
		return incusprovider.ExecNodeResult{}, fmt.Errorf("unexpected Node exec request: %#v", request)
	}
	command := request.Command[1]
	switch {
	case strings.HasSuffix(command, "/answer.sh"):
		e.commands = append(e.commands, "answer.sh")
		return incusprovider.ExecNodeResult{ExitCode: 0}, nil
	case strings.HasSuffix(command, "/checks.sh"):
		e.commands = append(e.commands, "checks.sh")
		return incusprovider.ExecNodeResult{ExitCode: 0, Stdout: e.checks}, nil
	default:
		return incusprovider.ExecNodeResult{}, fmt.Errorf("unexpected Node command %q", command)
	}
}

func nodeVerificationClaim() candidateworker.Claim {
	deadline := time.Now().UTC().Add(time.Minute)
	fingerprint := strings.Repeat("a", 64)
	return candidateworker.Claim{
		Work: workClaim("verify-work", "candidate-verify", deadline),
		Candidate: candidate.WorkerView{
			ID:            "candidate-verify",
			ArchiveSHA256: "sha256:" + strings.Repeat("b", 64),
			State:         candidate.StateVerifying,
			Artifact: &candidate.ArtifactReference{
				Runtime: challenge.RuntimeNode, IncusAlias: "candidate-verify", IncusFingerprint: fingerprint,
			},
			Snapshot: candidate.ExecutionSnapshot{
				Runtime:     challenge.RuntimeNode,
				Checkpoints: []candidate.CheckpointSnapshot{{ID: "service-ready", Node: "host"}},
				Node: &candidate.NodeRuntimeSnapshot{
					BaseImageFingerprint: fingerprint, ProfileRevision: "node-profile-v1", NetworkPolicyRevision: "network-v1",
					Nodes:     []candidate.NodeSnapshot{{Name: "host", Title: "Host"}},
					Resources: candidate.NodeResources{CPU: "1", Memory: "512MiB", Processes: 512, RootDisk: "5GiB"},
				},
			},
		},
	}
}

func workClaim(id, candidateID string, deadline time.Time) worklist.Claim {
	return worklist.Claim{
		Item: worklist.Item{
			ID: id, Kind: worklist.KindVerify, SubjectType: worklist.SubjectCandidateRevision, SubjectID: candidateID,
			State: worklist.StateRunning, Attempt: 1, DeadlineAt: &deadline,
		},
		LeaseOwner: "verifier-test",
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
