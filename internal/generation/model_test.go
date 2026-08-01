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
