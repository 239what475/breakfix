package authoring

import "testing"

func TestInfrastructureFailedSessionCanCreateAReplacementPlan(t *testing.T) {
	if !AllowsAgentPlanStage(StateInfrastructureFailed) {
		t.Fatal("infrastructure-failed session cannot accept an authoring message")
	}
	if state := NextPlanRevisionState(StateInfrastructureFailed); state != StateRevisingAndVerifying {
		t.Fatalf("next state = %q, want %q", state, StateRevisingAndVerifying)
	}
}
