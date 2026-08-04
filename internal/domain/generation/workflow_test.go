package generation

import "testing"

func TestGenerationStateClassification(t *testing.T) {
	if !StateGenerating.Leaseable() || !StateGenerating.DeadlineActive() {
		t.Fatal("active generation state must be leaseable and consume its deadline")
	}
	if !StateClassifying.Leaseable() || !StateClassifying.DeadlineActive() {
		t.Fatal("classification must be leaseable and consume its own execution window")
	}
	if StateNeedsAuthorReview.Leaseable() || StateNeedsAuthorReview.DeadlineActive() ||
		StateNeedsClassificationReview.Leaseable() || StateNeedsClassificationReview.DeadlineActive() {
		t.Fatal("author review states must not hold a lease or consume execution time")
	}
	if !StatePublished.Terminal() || StatePublished.Leaseable() || !StateSuperseded.Terminal() {
		t.Fatal("published and superseded workflows must be terminal")
	}
}

func TestWorkflowSourceRequiresAuthoringLineage(t *testing.T) {
	authoring := Workflow{
		ID: "generation-authoring", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session"},
		SourceRevision: "2", State: StateGenerating,
	}
	if !authoring.Valid() {
		t.Fatalf("valid authoring workflow rejected: %#v", authoring)
	}
	authoring.SourceRevision = ""
	if authoring.Valid() {
		t.Fatal("workflow accepted an empty source revision")
	}
}
