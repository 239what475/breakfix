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

func TestWorkflowSourceKeepsAuthoringAndReleaseLineageDistinct(t *testing.T) {
	authoring := Workflow{
		ID: "generation-authoring", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session"},
		AuthoringRevision: 2, State: StateQueued,
	}
	if !authoring.Valid() {
		t.Fatalf("valid authoring workflow rejected: %#v", authoring)
	}
	release := Workflow{
		ID: "generation-release", Source: Source{Kind: SourceRelease, Ref: "catalog-entry"},
		State: StateQueued,
	}
	if !release.Valid() {
		t.Fatalf("valid release workflow rejected: %#v", release)
	}
	release.AuthoringRevision = 1
	if release.Valid() {
		t.Fatal("release workflow accepted an authoring revision")
	}
}
