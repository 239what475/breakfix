package generation

import "testing"

func TestGenerationStateClassification(t *testing.T) {
	if !StateGenerating.Leaseable() || !StateGenerating.DeadlineActive() {
		t.Fatal("active generation state must be leaseable and consume its deadline")
	}
	if StateNeedsAuthorReview.Leaseable() || StateNeedsAuthorReview.DeadlineActive() {
		t.Fatal("author review must not hold a lease or consume execution time")
	}
	if !StateCleaningUp.Leaseable() || StateCleaningUp.DeadlineActive() {
		t.Fatal("cleanup must be recoverable without the expired execution deadline")
	}
	if !StateCompleted.Terminal() || StateCompleted.Leaseable() {
		t.Fatal("completed workflow must be terminal")
	}
}

func TestWorkflowSourceRequiresAuthoringLineage(t *testing.T) {
	authoring := Workflow{
		ID: "generation-authoring", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session"},
		SourceRevision: "2", State: StateQueued,
	}
	if !authoring.Valid() {
		t.Fatalf("valid authoring workflow rejected: %#v", authoring)
	}
	authoring.SourceRevision = ""
	if authoring.Valid() {
		t.Fatal("workflow accepted an empty source revision")
	}
}
