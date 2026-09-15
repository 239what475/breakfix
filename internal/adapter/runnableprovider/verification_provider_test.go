package runnableprovider

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	runnableworker "github.com/breakfix/breakfix/internal/worker/runnable"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestVerificationProviderCreatesAndPollsReadyEnvironment(t *testing.T) {
	revision := verificationRevision(t, runnable.RuntimeNode)
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeRuntimeEnvironmentClient{
		created: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}},
		observations: []*runtimev2.RuntimeEnvironment{
			{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseProvisioning}},
			{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{Provider: "node", ProfileDigest: profileDigest}}},
		},
	}
	provider, err := NewVerificationProvider(client, client, &fakeNodeRuntimeExecutor{}, "breakfix-system")
	if err != nil {
		t.Fatal(err)
	}
	provider.pollEvery = time.Millisecond
	request := verificationRequest(t, revision)
	identity, err := provider.CreateVerificationEnvironment(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "env-uid" || identity.Provider != "node" || identity.ProfileDigest != profileDigest {
		t.Fatalf("identity = %#v", identity)
	}
	if client.createCalls != 1 || client.requested.RunnableRevisionRef.ID != request.RunnableRevisionRef.ID || client.requested.RunnableRevisionRef.Digest != request.RunnableRevisionDigest || client.requested.Attempt != request.Attempt {
		t.Fatalf("create request = %#v calls=%d", client.requested, client.createCalls)
	}
}

func TestVerificationProviderPollsServerProvisionedEnvironmentOnRetry(t *testing.T) {
	revision := verificationRevision(t, runnable.RuntimeNode)
	digest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := verificationRequest(t, revision)
	name := runnable.VerificationEnvironmentName(request.RunnableRevisionRef, request.Attempt)
	existing := &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("env-existing")}, Spec: runtimev2.RuntimeEnvironmentSpec{RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: request.RunnableRevisionRef.ID, Digest: request.RunnableRevisionDigest}, Purpose: runtimev2.PurposeVerification}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{Provider: "node", ProfileDigest: digest}}}
	client := &fakeRuntimeEnvironmentClient{created: existing, getResult: existing}
	provider, err := NewVerificationProvider(client, client, &fakeNodeRuntimeExecutor{}, "breakfix-system")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.CreateVerificationEnvironment(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ID != "env-existing" || client.getCalls != 1 || client.createCalls != 1 {
		t.Fatalf("adopted identity=%#v get calls=%d", identity, client.getCalls)
	}
}

func TestVerificationProviderRejectsUIDAndProfileChanges(t *testing.T) {
	tests := []struct {
		name  string
		ready *runtimev2.RuntimeEnvironment
		want  string
	}{
		{name: "uid", ready: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("other")}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady}}, want: "UID changed"},
		{name: "profile", ready: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{Provider: "node", ProfileDigest: testDigest("f")}}}, want: "another profile"},
		{name: "provider", ready: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{Provider: "k8s", ProfileDigest: ""}}}, want: "another profile"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			revision := verificationRevision(t, runnable.RuntimeNode)
			client := &fakeRuntimeEnvironmentClient{created: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}}, observations: []*runtimev2.RuntimeEnvironment{test.ready}}
			provider, err := NewVerificationProvider(client, client, &fakeNodeRuntimeExecutor{}, "breakfix-system")
			if err != nil {
				t.Fatal(err)
			}
			provider.pollEvery = time.Millisecond
			_, err = provider.CreateVerificationEnvironment(context.Background(), verificationRequest(t, revision))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestVerificationProviderExecutesNodeTarget(t *testing.T) {
	revision := verificationRevision(t, runnable.RuntimeNode)
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeRuntimeEnvironmentClient{
		created: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}},
		observations: []*runtimev2.RuntimeEnvironment{{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")},
			Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{
				Provider: "node", ProfileDigest: profileDigest,
				ResourceRefs: []runtimev2.ResourceReference{{Kind: "project", ID: "project"}, {Kind: "network", ID: "network"}, {Kind: "acl", ID: "acl"}, {Kind: "profile", ID: "profile"}, {Kind: "instance:host", ID: "instance-host"}},
			}},
		}},
	}
	node := &fakeNodeRuntimeExecutor{result: incus.ExecNodeResult{Stdout: "ok\n", Stderr: "diag\n", ExitCode: 0}}
	provider, err := NewVerificationProvider(client, client, node, "breakfix-system")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.CreateVerificationEnvironment(context.Background(), verificationRequest(t, revision))
	if err != nil {
		t.Fatal(err)
	}
	output, err := provider.Execute(context.Background(), identity, runnableworker.ExecutionRequest{Entrypoint: "scripts/check.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Boundary: runnable.ExecutionBoundary{ID: "write", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 30}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if output.ExitCode != 0 || string(output.Raw) != "ok\n" || string(output.Stderr) != "diag\n" || node.request.LogicalName != "host" || strings.Join(node.request.Command, " ") != "/bin/bash /opt/breakfix/runnable/scripts/check.sh" {
		t.Fatalf("output=%#v request=%#v", output, node.request)
	}
	if len(output.Outputs) != 0 {
		t.Fatalf("provider unexpectedly created output references: %#v", output.Outputs)
	}
}

func TestVerificationProviderExecutesK8sManagementTarget(t *testing.T) {
	revision := verificationRevision(t, runnable.RuntimeK8s)
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeRuntimeEnvironmentClient{created: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}}, observations: []*runtimev2.RuntimeEnvironment{{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{Provider: "k8s", ProfileDigest: profileDigest, EndpointRefs: []runtimev2.EndpointReference{{Name: "terminal", Ref: "runtime-ns/runtime-terminal"}}}}}}, execResult: kubernetes.PodExecResult{ExitCode: 0, Stdout: `{"assertions":[]}`, Stderr: "diagnostic"}}
	provider, err := NewVerificationProvider(client, client, nil, "breakfix-system")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.CreateVerificationEnvironment(context.Background(), verificationRequest(t, revision))
	if err != nil {
		t.Fatal(err)
	}
	output, err := provider.Execute(context.Background(), identity, runnableworker.ExecutionRequest{Entrypoint: "scripts/check.sh", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Boundary: runnable.ExecutionBoundary{ID: "read", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 30}, Timeout: time.Second, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if output.ExitCode != 0 || string(output.Raw) != `{"assertions":[]}` || string(output.Stderr) != "diagnostic" || client.execNamespace != "runtime-ns" || client.execPod != "runtime-terminal" {
		t.Fatalf("output=%#v exec=%s/%s", output, client.execNamespace, client.execPod)
	}
}

func TestVerificationProviderRejectsUnsafeEntrypointAndMissingNodeTarget(t *testing.T) {
	revision := verificationRevision(t, runnable.RuntimeNode)
	digest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeRuntimeEnvironmentClient{created: &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}}, observations: []*runtimev2.RuntimeEnvironment{{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}, Status: runtimev2.RuntimeEnvironmentStatus{Phase: runtimev2.PhaseReady, Runtime: runtimev2.RuntimeStatus{Provider: "node", ProfileDigest: digest}}}}}
	provider, err := NewVerificationProvider(client, client, &fakeNodeRuntimeExecutor{}, "breakfix-system")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := provider.CreateVerificationEnvironment(context.Background(), verificationRequest(t, revision))
	if err != nil {
		t.Fatal(err)
	}
	for _, entrypoint := range []string{"../escape.sh", "/absolute.sh"} {
		if _, err := provider.Execute(context.Background(), identity, runnableworker.ExecutionRequest{Entrypoint: entrypoint, Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Boundary: runnable.ExecutionBoundary{ID: "write", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 30}, Timeout: time.Second}); err == nil {
			t.Fatalf("entrypoint %q was accepted", entrypoint)
		}
	}
	if _, err := provider.Execute(context.Background(), identity, runnableworker.ExecutionRequest{Entrypoint: "scripts/check.sh", Target: runnable.TargetLocation{Kind: "node", ID: "missing"}, Boundary: runnable.ExecutionBoundary{ID: "write", Target: runnable.TargetLocation{Kind: "node", ID: "missing"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 30}, Timeout: time.Second}); err == nil {
		t.Fatal("missing Node target was accepted")
	}
}

type fakeRuntimeEnvironmentClient struct {
	created       *runtimev2.RuntimeEnvironment
	requested     runnable.VerifyRequest
	getResult     *runtimev2.RuntimeEnvironment
	observations  []*runtimev2.RuntimeEnvironment
	execResult    kubernetes.PodExecResult
	createCalls   int
	getCalls      int
	execNamespace string
	execPod       string
}

func (c *fakeRuntimeEnvironmentClient) CreateVerificationEnvironment(_ context.Context, request runnable.VerifyRequest) (*runtimev2.RuntimeEnvironment, error) {
	c.createCalls++
	c.requested = request
	if c.created == nil {
		c.created = &runtimev2.RuntimeEnvironment{ObjectMeta: metav1.ObjectMeta{UID: types.UID("env-uid")}}
	}
	created := c.created.DeepCopy()
	created.Name = runnable.VerificationEnvironmentName(request.RunnableRevisionRef, request.Attempt)
	created.Spec = runtimev2.RuntimeEnvironmentSpec{
		RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: request.RunnableRevisionRef.ID, Digest: request.RunnableRevisionDigest},
		Purpose:             runtimev2.PurposeVerification,
	}
	return created, nil
}

func (c *fakeRuntimeEnvironmentClient) GetRuntimeEnvironment(_ context.Context, _ string, _ string) (*runtimev2.RuntimeEnvironment, error) {
	c.getCalls++
	if len(c.observations) > 0 {
		observation := c.observations[0]
		c.observations = c.observations[1:]
		return observation.DeepCopy(), nil
	}
	if c.getResult != nil {
		return c.getResult.DeepCopy(), nil
	}
	return c.created.DeepCopy(), nil
}

func (c *fakeRuntimeEnvironmentClient) ExecInPodStreamsContext(_ context.Context, namespace, pod string, _ int, _ ...string) (kubernetes.PodExecResult, error) {
	c.execNamespace, c.execPod = namespace, pod
	return c.execResult, nil
}

type fakeNodeRuntimeExecutor struct {
	result  incus.ExecNodeResult
	request incus.ExecNodeRequest
}

func (e *fakeNodeRuntimeExecutor) ExecNode(_ context.Context, request incus.ExecNodeRequest) (incus.ExecNodeResult, error) {
	e.request = request
	return e.result, nil
}

func verificationRequest(t *testing.T, revision runnable.RunnableRevision) runnable.VerifyRequest {
	t.Helper()
	specDigest, err := revision.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return runnable.VerifyRequest{Credential: runnable.LeaseCredential{Identity: runnable.ActionIdentity{Content: revision.Spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: 1}, LeaseOwner: "worker-01"}, RunnableRevision: revision, RunnableRevisionRef: runnable.RevisionReference{ID: "revision-01", Digest: revisionDigest}, RunnableRevisionDigest: revisionDigest, Attempt: 1}
}

func verificationRevision(t *testing.T, runtimeType runnable.Runtime) runnable.RunnableRevision {
	t.Helper()
	targetKind, targetID := "node", "host"
	if runtimeType == runnable.RuntimeK8s {
		targetKind, targetID = "management", "cluster"
	}
	profile := runnable.RuntimeProfile{Runtime: runtimeType, ProfileRevision: "profile-01", BaseImage: "registry.example/base@" + testDigest("a"), SoftwareVersions: map[string]string{"runtime": "v1"}, Resources: runnable.ResourceLimits{CPU: "1", MemoryBytes: 1, EphemeralBytes: 1, MaxProcesses: 1, MaxConcurrentTasks: 1}, Network: runnable.NetworkPrivate, Topology: "single", ExecutionBoundaries: []runnable.ExecutionBoundary{{ID: "write", Target: runnable.TargetLocation{Kind: targetKind, ID: targetID}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 30}, {ID: "read", Target: runnable.TargetLocation{Kind: targetKind, ID: targetID}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 30}}}
	spec := runnable.RunnableSpec{FormatVersion: runnable.FormatVersion, Identity: runnable.ContentIdentity{Kind: "operations", ID: "practice", Revision: "revision-01"}, RuntimeProfile: profile, Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/practice.tar.gz", Digest: testDigest("b")}, Initialization: []runnable.ActionSpec{{ID: "initialize", Entrypoint: "scripts/init.sh", Target: runnable.TargetLocation{Kind: targetKind, ID: targetID}, BoundaryID: "write", TimeoutSeconds: 10, ExpectedExitCodes: []int{0}}}, ValidationPlan: runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{{ID: "observe", TimeoutSeconds: 20, Execution: runnable.PhaseSequential, Assertions: []runnable.AssertionSpec{{ID: "ready", Entrypoint: "scripts/check.sh", Target: runnable.TargetLocation{Kind: targetKind, ID: targetID}, BoundaryID: "read", TimeoutSeconds: 10}}}}}, LifecyclePolicy: runnable.LifecyclePolicy{CreateTimeoutSeconds: 30, ResetTimeoutSeconds: 30, StopTimeoutSeconds: 10, ReapTimeoutSeconds: 10, IdleTTLSeconds: 30, MaxLifetimeSeconds: 60}}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: runtimeType, ProviderReference: "registry.example/practice@" + testDigest("c"), ArtifactDigest: testDigest("c"), BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01"}}
}

func testDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
