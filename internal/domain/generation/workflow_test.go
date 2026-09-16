package generation

import "testing"

func TestGenerationStatesHaveOneExecutor(t *testing.T) {
	if StateGenerating.AgentState() || StateGenerating.RunnableState() {
		t.Fatal("Generating must be user-directed workspace activity")
	}
	if !StateMaterializingArtifact.RunnableState() || !StateVerifying.RunnableState() || StateMaterializingArtifact.AgentState() {
		t.Fatal("public runnable states must not be claimed by the generation Agent")
	}
	if StateNeedsAuthorReview.AgentState() || StateNeedsAuthorReview.RunnableState() {
		t.Fatal("author review states must have no active executor")
	}
	if !StatePublished.Terminal() || !StateCancelled.Terminal() {
		t.Fatal("published and cancelled workflows must be terminal")
	}
}

func TestWorkflowSourceRequiresAuthoringLineage(t *testing.T) {
	authoring := Workflow{
		ID: "generation-authoring", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session"},
		SourceRevision: "2", State: StateGenerating, StateVersion: 1,
	}
	if !authoring.Valid() {
		t.Fatalf("valid authoring workflow rejected: %#v", authoring)
	}
	authoring.SourceRevision = ""
	if authoring.Valid() {
		t.Fatal("workflow accepted an empty source revision")
	}
}

func TestClaimRequiresTheWorkflowStateVersion(t *testing.T) {
	workflow := Workflow{
		ID: "generation-claim", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session"},
		SourceRevision: "2", State: StateGenerating, StateVersion: 3,
	}
	claim := Claim{Workflow: workflow, LeaseCredential: LeaseCredential{StateVersion: 2, LeaseOwner: "server-lease"}}
	if claim.Valid() {
		t.Fatal("claim accepted a credential from an older workflow state")
	}
	claim.StateVersion = workflow.StateVersion
	if !claim.Valid() {
		t.Fatalf("claim rejected matching state version: %#v", claim)
	}
}

func TestJudgingWorkflowMayWaitWithoutAnAgentLease(t *testing.T) {
	workflow := Workflow{
		ID: "generation-judging", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session"},
		SourceRevision: "2", State: StateJudging, StateVersion: 1,
	}
	if !workflow.Valid() {
		t.Fatalf("unclaimed judging workflow is invalid: %#v", workflow)
	}
}
