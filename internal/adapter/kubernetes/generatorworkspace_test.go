package kubernetes

import (
	"context"
	"testing"

	apiv2 "github.com/breakfix/breakfix/api/v2"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newGeneratorWorkspaceOwner(t *testing.T, objects ...client.Object) *GeneratorWorkspaceOwner {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := apiv2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &GeneratorWorkspaceOwner{
		client:    fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&apiv2.GeneratorWorkspace{}).WithObjects(objects...).Build(),
		namespace: "opensandbox",
	}
}

func testWorkspaceRecord(id string) domain.Workspace {
	return domain.Workspace{
		ID: id, WorkflowID: "workflow-" + id, Namespace: "opensandbox", PVCName: "breakfix-workspace-" + id,
		SandboxID: "sandbox-" + id, State: domain.WorkspacePending,
	}
}

func TestEnsureWorkspaceOwnerCreatesWithFinalizerAndReference(t *testing.T) {
	owner := newGeneratorWorkspaceOwner(t)
	record := testWorkspaceRecord("generator-workspace-one")

	reference, err := owner.EnsureWorkspaceOwner(context.Background(), record)
	if err != nil {
		t.Fatalf("ensure workspace owner: %v", err)
	}
	if reference.APIVersion != "breakfix.dev/v2" || reference.Kind != "GeneratorWorkspace" || reference.Name != record.ID {
		t.Fatalf("owner reference = %#v", reference)
	}
	var current apiv2.GeneratorWorkspace
	if err := owner.client.Get(context.Background(), client.ObjectKey{Namespace: "opensandbox", Name: record.ID}, &current); err != nil {
		t.Fatalf("get created owner: %v", err)
	}
	if !hasGeneratorWorkspaceFinalizer(&current) {
		t.Fatalf("created owner has no cleanup finalizer: %#v", current.Finalizers)
	}
	if current.Status.Phase != apiv2.WorkspacePhasePending || current.Status.WorkflowID != record.WorkflowID {
		t.Fatalf("created owner status = %#v", current.Status)
	}
	// Ensuring again must stay idempotent and keep returning the reference.
	again, err := owner.EnsureWorkspaceOwner(context.Background(), record)
	if err != nil || again.Name != reference.Name {
		t.Fatalf("second ensure = %#v, %v", again, err)
	}
}

func TestEnsureWorkspaceOwnerPropagatesExistingIdentity(t *testing.T) {
	existing := &apiv2.GeneratorWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "generator-workspace-one", Namespace: "opensandbox", UID: "uid-existing",
			Finalizers: []string{apiv2.GeneratorWorkspaceCleanupFinalizer},
		},
		Spec: apiv2.GeneratorWorkspaceSpec{},
		Status: apiv2.GeneratorWorkspaceStatus{
			WorkflowID: "workflow-generator-workspace-one", Namespace: "opensandbox",
			PVCName: "breakfix-workspace-generator-workspace-one", Phase: apiv2.WorkspacePhasePending,
		},
	}
	owner := newGeneratorWorkspaceOwner(t, existing)

	reference, err := owner.EnsureWorkspaceOwner(context.Background(), testWorkspaceRecord("generator-workspace-one"))
	if err != nil {
		t.Fatalf("ensure existing workspace owner: %v", err)
	}
	if reference.UID != "uid-existing" {
		t.Fatalf("owner reference UID = %q, want uid-existing", reference.UID)
	}
}

func TestRecordWorkspaceOwnerStatusBackfillsActiveFacts(t *testing.T) {
	owner := newGeneratorWorkspaceOwner(t)
	record := testWorkspaceRecord("generator-workspace-one")
	if _, err := owner.EnsureWorkspaceOwner(context.Background(), record); err != nil {
		t.Fatalf("ensure workspace owner: %v", err)
	}

	record.State = domain.WorkspaceActive
	if err := owner.RecordWorkspaceOwnerStatus(context.Background(), record); err != nil {
		t.Fatalf("record workspace owner status: %v", err)
	}
	var current apiv2.GeneratorWorkspace
	if err := owner.client.Get(context.Background(), client.ObjectKey{Namespace: "opensandbox", Name: record.ID}, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != apiv2.WorkspacePhaseActive || current.Status.SandboxID != record.SandboxID || current.Status.PVCName != record.PVCName {
		t.Fatalf("backfilled status = %#v", current.Status)
	}
}

func TestRecordWorkspaceOwnerStatusRecreatesLostOwner(t *testing.T) {
	owner := newGeneratorWorkspaceOwner(t)
	record := testWorkspaceRecord("generator-workspace-one")
	record.State = domain.WorkspaceActive

	if err := owner.RecordWorkspaceOwnerStatus(context.Background(), record); err != nil {
		t.Fatalf("record workspace owner status on lost CR: %v", err)
	}
	var current apiv2.GeneratorWorkspace
	if err := owner.client.Get(context.Background(), client.ObjectKey{Namespace: "opensandbox", Name: record.ID}, &current); err != nil {
		t.Fatal(err)
	}
	if !hasGeneratorWorkspaceFinalizer(&current) || current.Status.Phase != apiv2.WorkspacePhaseActive {
		t.Fatalf("recreated owner = %#v %#v", current.Finalizers, current.Status)
	}
}

func TestRetireAndDeleteWorkspaceOwnerFollowTheFinalizerContract(t *testing.T) {
	owner := newGeneratorWorkspaceOwner(t)
	record := testWorkspaceRecord("generator-workspace-one")
	if _, err := owner.EnsureWorkspaceOwner(context.Background(), record); err != nil {
		t.Fatalf("ensure workspace owner: %v", err)
	}

	if err := owner.RetireWorkspaceOwner(context.Background(), record.ID); err != nil {
		t.Fatalf("retire workspace owner: %v", err)
	}
	var current apiv2.GeneratorWorkspace
	if err := owner.client.Get(context.Background(), client.ObjectKey{Namespace: "opensandbox", Name: record.ID}, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != apiv2.WorkspacePhaseDeleting {
		t.Fatalf("retired phase = %q", current.Status.Phase)
	}
	if !hasGeneratorWorkspaceFinalizer(&current) {
		t.Fatal("retired owner lost its cleanup finalizer")
	}
	if err := owner.DeleteWorkspaceOwner(context.Background(), record.ID); err != nil {
		t.Fatalf("delete workspace owner: %v", err)
	}
	if err := owner.client.Get(context.Background(), client.ObjectKey{Namespace: "opensandbox", Name: record.ID}, &current); err == nil {
		t.Fatal("deleted owner still exists")
	}
	// Deleting an already-gone owner is part of the idempotent cleanup flow.
	if err := owner.DeleteWorkspaceOwner(context.Background(), record.ID); err != nil {
		t.Fatalf("delete missing workspace owner: %v", err)
	}
	if err := owner.RetireWorkspaceOwner(context.Background(), "generator-workspace-missing"); err != nil {
		t.Fatalf("retire missing workspace owner: %v", err)
	}
}

func TestListWorkspaceOwnersProjectsOwnershipFacts(t *testing.T) {
	owner := newGeneratorWorkspaceOwner(t)
	first := testWorkspaceRecord("generator-workspace-one")
	second := testWorkspaceRecord("generator-workspace-two")
	second.State = domain.WorkspaceDeleting
	for _, record := range []domain.Workspace{first, second} {
		if _, err := owner.EnsureWorkspaceOwner(context.Background(), record); err != nil {
			t.Fatalf("ensure workspace owner: %v", err)
		}
		if err := owner.RecordWorkspaceOwnerStatus(context.Background(), record); err != nil {
			t.Fatalf("record workspace owner status: %v", err)
		}
	}

	owners, err := owner.ListWorkspaceOwners(context.Background())
	if err != nil {
		t.Fatalf("list workspace owners: %v", err)
	}
	if len(owners) != 2 {
		t.Fatalf("listed owners = %#v", owners)
	}
	states := make(map[string]domain.WorkspaceState, len(owners))
	for _, record := range owners {
		if !record.Rebuildable() {
			t.Fatalf("listed owner is not rebuildable: %#v", record)
		}
		states[record.ID] = record.State
	}
	if states["generator-workspace-one"] != domain.WorkspacePending || states["generator-workspace-two"] != domain.WorkspaceDeleting {
		t.Fatalf("listed states = %#v", states)
	}
}

func TestGeneratorWorkspaceOwnerRequiresConfiguration(t *testing.T) {
	if _, err := NewGeneratorWorkspaceOwner(nil, "opensandbox"); err == nil {
		t.Fatal("missing rest config unexpectedly accepted")
	}
	if _, err := NewGeneratorWorkspaceOwner(nil, " "); err == nil {
		t.Fatal("missing namespace unexpectedly accepted")
	}
	var unconfigured *GeneratorWorkspaceOwner
	if _, err := unconfigured.EnsureWorkspaceOwner(context.Background(), testWorkspaceRecord("generator-workspace-one")); err == nil {
		t.Fatal("nil client unexpectedly accepted")
	}
	if _, err := unconfigured.ListWorkspaceOwners(context.Background()); err == nil {
		t.Fatal("nil client list unexpectedly accepted")
	}
}
