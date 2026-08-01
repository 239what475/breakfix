package taxonomy

import "testing"

func TestTaxonomyReviewValidation(t *testing.T) {
	if err := ValidateReview(Review{Decision: ReviewApprove}); err != nil {
		t.Fatalf("approve review = %v", err)
	}
	if err := ValidateReview(Review{Decision: ReviewReject, Feedback: "mapping omits a prerequisite"}); err != nil {
		t.Fatalf("reject review = %v", err)
	}
}
