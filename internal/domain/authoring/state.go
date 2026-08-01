package authoring

// AllowsAuthorMessage reports whether a user may start another authoring turn
// for the session. Generation lifecycle is deliberately owned elsewhere.
func AllowsAuthorMessage(state SessionState) bool {
	switch state {
	case StateDraftConversation, StateIntentReview:
		return true
	default:
		return false
	}
}

// AllowsAgentPlanStage reports whether an Agent Run may mutate its private
// staged Plan. Public revisions are created only by finalization.
func AllowsAgentPlanStage(state SessionState) bool {
	return AllowsAuthorMessage(state)
}

// NextPlanRevisionState intentionally does not mirror GenerationWorkflow.
// Workflow state is the only execution lifecycle authority.
func NextPlanRevisionState(state SessionState) SessionState {
	_ = state
	return StateIntentReview
}
