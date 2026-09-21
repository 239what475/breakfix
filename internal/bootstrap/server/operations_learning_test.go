package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestOperationsLearningEvaluatorExecutesBoundFinalAssertion(t *testing.T) {
	revision := operationsLearningRevision(t)
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	environment := operationsLearningEnvironment(digest, profileDigest)
	node := &learningNodeExecutor{result: incus.ExecNodeResult{Stdout: `{"assertions":[{"id":"ready","satisfied":true,"summary":"ready"}]}`}}
	now := time.Date(2026, time.September, 15, 7, 0, 0, 0, time.UTC)
	evaluator := operationsLearningCheckpointEvaluator{revisions: staticRunnableRevision{revision: revision}, node: node, now: func() time.Time { return now }}

	checkpoints, err := evaluator.Evaluate(context.Background(), environment)
	if err != nil {
		t.Fatalf("evaluate Operations checkpoints: %v", err)
	}
	if len(checkpoints) != 1 || !checkpoints[0].Passed || checkpoints[0].FirstPassedAt == nil || !checkpoints[0].FirstPassedAt.Equal(now) {
		t.Fatalf("checkpoints = %#v", checkpoints)
	}
	if len(node.requests) != 1 || len(node.requests[0].Command) != 2 || node.requests[0].Command[0] != "/bin/bash" || node.requests[0].Command[1] != "/opt/breakfix/runnable/nodes/host/assertions/final-ready.sh" {
		t.Fatalf("learning executor ran an unexpected command: %#v", node.requests)
	}
}

func TestOperationsLearningEvaluatorRejectsMismatchedProfileBeforeExecution(t *testing.T) {
	revision := operationsLearningRevision(t)
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	environment := operationsLearningEnvironment(digest, "sha256:"+strings.Repeat("f", 64))
	node := &learningNodeExecutor{}
	evaluator := operationsLearningCheckpointEvaluator{revisions: staticRunnableRevision{revision: revision}, node: node}

	if _, err := evaluator.Evaluate(context.Background(), environment); err == nil || !strings.Contains(err.Error(), "another runtime profile") {
		t.Fatalf("mismatched runtime profile was accepted: %v", err)
	}
	if len(node.requests) != 0 {
		t.Fatalf("mismatched profile executed a command: %#v", node.requests)
	}
}

type staticRunnableRevision struct{ revision runnable.RunnableRevision }

func (s staticRunnableRevision) ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error) {
	return s.revision, nil
}

type learningNodeExecutor struct {
	requests []incus.ExecNodeRequest
	result   incus.ExecNodeResult
}

func (e *learningNodeExecutor) ExecNode(_ context.Context, request incus.ExecNodeRequest) (incus.ExecNodeResult, error) {
	e.requests = append(e.requests, request)
	return e.result, nil
}

func operationsLearningEnvironment(revisionDigest, profileDigest string) runtimev2.RuntimeEnvironment {
	return runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("environment-01"), Labels: map[string]string{"breakfix.dev/content-kind": "operations"}},
		Spec: runtimev2.RuntimeEnvironmentSpec{
			RunnableRevisionRef: &runtimev2.RunnableRevisionReference{ID: "rr-demo", Digest: revisionDigest}, Purpose: runtimev2.PurposeLearning,
		},
		Status: runtimev2.RuntimeEnvironmentStatus{
			Phase: runtimev2.PhaseReady,
			Runtime: runtimev2.RuntimeStatus{Provider: "node", ProfileDigest: profileDigest, ResourceRefs: []runtimev2.ResourceReference{
				{Kind: "project", ID: "project-01"}, {Kind: "network", ID: "network-01"}, {Kind: "acl", ID: "acl-01"}, {Kind: "profile", ID: "profile-01"}, {Kind: "instance:host", ID: "host-01"},
			}},
		},
	}
}

func operationsLearningRevision(t *testing.T) runnable.RunnableRevision {
	t.Helper()
	entry := scenario.Entry{
		Type: scenario.ScenarioOperationsScenario, Runtime: scenario.RuntimeNode, Topology: "one host",
		Versions:     []scenario.Version{{Component: "fixture", Version: "v1"}},
		Reproduction: scenario.Reproduction{Evidence: []scenario.ReproductionEvidence{{ID: "missing", Node: "host"}}},
		Nodes:        []scenario.Node{{Name: "host", Title: "Host"}},
		Checkpoints:  []scenario.Checkpoint{{ID: "ready", Node: "host"}}, HasReferenceRepair: true,
	}
	config := operations.Config{
		MaxNodes:  1,
		Node:      operations.RuntimeProfileConfig{ProfileRevision: "profile-01", BaseImage: "registry.example/base@sha256:" + strings.Repeat("a", 64), Resources: runnable.ResourceLimits{CPU: "1", MemoryBytes: 1 << 30, EphemeralBytes: 2 << 30, MaxProcesses: 32, MaxConcurrentTasks: 1}, Network: runnable.NetworkPrivate, MaxActionTimeout: 1200},
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 300, ResetTimeoutSeconds: 300, StopTimeoutSeconds: 300, ReapTimeoutSeconds: 300, IdleTTLSeconds: 600, MaxLifetimeSeconds: 1800},
	}
	spec, err := operations.Compile(operations.Input{ContentID: "operations-demo", ContentRevision: "revision-01", Entry: entry, Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/demo.tar.gz", Digest: "sha256:" + strings.Repeat("b", 64)}}, config)
	if err != nil {
		t.Fatal(err)
	}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{
		FormatVersion: runnable.FormatVersion, Runtime: runnable.RuntimeNode, ProviderReference: "registry.example/demo@sha256:" + strings.Repeat("c", 64), ArtifactDigest: "sha256:" + strings.Repeat("c", 64), BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01",
	}}
	if err := revision.Validate(); err != nil {
		t.Fatal(fmt.Errorf("validate test runnable revision: %w", err))
	}
	return revision
}
