package authoring

import (
	"strings"
	"testing"
)

func TestStateAllowsAuthorMessage(t *testing.T) {
	tests := []struct {
		state SessionState
		want  bool
	}{
		{StateDraftConversation, true},
		{StateIntentReview, true},
		{StateAwaitingVerifiedReview, true},
		{StateGeneratingAndVerifying, false},
		{StateRevisingAndVerifying, false},
		{StatePublishing, false},
		{StatePublished, false},
	}
	for _, test := range tests {
		if got := stateAllowsAuthorMessage(test.state); got != test.want {
			t.Errorf("stateAllowsAuthorMessage(%q) = %t, want %t", test.state, got, test.want)
		}
	}
}

func TestStateAllowsAgentPlanRevisionWithinOneAuthorTurn(t *testing.T) {
	if !stateAllowsAgentPlanRevision(StateRevisingAndVerifying) {
		t.Fatal("agent must be able to complete the remaining function calls in a revision turn")
	}
	if stateAllowsAgentPlanRevision(StateGeneratingAndVerifying) {
		t.Fatal("agent must not edit a plan while an initial generation is running")
	}
}

func TestNextPlanRevisionState(t *testing.T) {
	tests := []struct {
		state SessionState
		want  SessionState
	}{
		{StateDraftConversation, StateIntentReview},
		{StateIntentReview, StateIntentReview},
		{StateAwaitingVerifiedReview, StateRevisingAndVerifying},
		{StateRevisingAndVerifying, StateRevisingAndVerifying},
	}
	for _, test := range tests {
		if got := nextPlanRevisionState(test.state); got != test.want {
			t.Errorf("nextPlanRevisionState(%q) = %q, want %q", test.state, got, test.want)
		}
	}
}

func TestVerificationFailureFeedbackIsInternalAndStructured(t *testing.T) {
	verification := Verification{
		TaskID:  "vt-test",
		Phase:   "Failed",
		Message: "verification did not complete",
		Report: &VerificationReport{
			Summary: "answer script left the service unavailable",
			Issues: []VerificationIssue{{
				Code:    "CHECKPOINTS_FAILED",
				Message: "service-ready did not pass",
			}},
		},
	}

	feedback := verification.Report.FailureFeedback()
	for _, expected := range []string{"真实验证未通过", "[CHECKPOINTS_FAILED] service-ready did not pass", "标准解答：未通过"} {
		if !strings.Contains(feedback, expected) {
			t.Errorf("failure feedback %q does not contain %q", feedback, expected)
		}
	}
}
