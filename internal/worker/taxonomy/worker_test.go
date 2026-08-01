package taxonomy

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	taxonomyapp "github.com/breakfix/breakfix/internal/application/taxonomy"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/taxonomy"
)

func TestWorkerPublishesApprovedTaxonomyWorkflow(t *testing.T) {
	store := newTaxonomyStore()
	committee := &scriptedCommittee{}
	worker := newTestTaxonomyWorker(t, store, committee)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process taxonomy workflow = %v, %v", processed, err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	wantStates := []taxonomy.WorkflowState{taxonomy.WorkflowMapping, taxonomy.WorkflowReviewing, taxonomy.WorkflowPublishing}
	if !workflowStatesEqual(store.phaseStates, wantStates) {
		t.Fatalf("phase states = %#v, want %#v", store.phaseStates, wantStates)
	}
	if store.current.Workflow.State != taxonomy.WorkflowCompleted || store.current.LeaseOwner != "" {
		t.Fatalf("completed taxonomy workflow = %#v, want completed without lease", store.current.Workflow)
	}
	wantRoles := []taxonomyapp.AgentRole{
		taxonomyapp.AgentRoleMapper,
		taxonomyapp.AgentRoleCurriculumReviewer,
		taxonomyapp.AgentRoleSREReviewer,
	}
	if !rolesEqual(store.roles, wantRoles) {
		t.Fatalf("agent roles = %#v, want %#v", store.roles, wantRoles)
	}
	if committee.mapCalls != 1 || committee.reviewCalls != 1 {
		t.Fatalf("committee calls = map %d review %d, want one each", committee.mapCalls, committee.reviewCalls)
	}
}

func TestWorkerReleasesRejectedCommitteeRoundForFreshMapperLease(t *testing.T) {
	store := newTaxonomyStore()
	committee := &rejectThenApproveCommittee{}
	worker := newTestTaxonomyWorker(t, store, committee)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process rejected taxonomy round = %v, %v", processed, err)
	}
	store.mu.Lock()
	if store.current.Workflow.State != taxonomy.WorkflowMapping || store.current.Workflow.Round != 1 || store.current.Workflow.LeaseOwner != "" {
		state := store.current.Workflow
		store.mu.Unlock()
		t.Fatalf("workflow after reject = %#v", state)
	}
	store.mu.Unlock()

	processed, err = worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process approved replacement round = %v, %v", processed, err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.current.Workflow.State != taxonomy.WorkflowCompleted || store.current.Workflow.Round != 1 {
		t.Fatalf("workflow after replacement round = %#v", store.current.Workflow)
	}
	if committee.mapCalls != 2 || committee.reviewCalls != 2 {
		t.Fatalf("committee calls = map %d review %d, want two each", committee.mapCalls, committee.reviewCalls)
	}
	wantStates := []taxonomy.WorkflowState{
		taxonomy.WorkflowMapping,
		taxonomy.WorkflowReviewing,
		taxonomy.WorkflowMapping,
		taxonomy.WorkflowReviewing,
		taxonomy.WorkflowPublishing,
	}
	if !workflowStatesEqual(store.phaseStates, wantStates) {
		t.Fatalf("phase states = %#v, want %#v", store.phaseStates, wantStates)
	}
}

type scriptedCommittee struct {
	mapCalls    int
	reviewCalls int
}

type rejectThenApproveCommittee struct {
	mapCalls    int
	reviewCalls int
}

func (c *rejectThenApproveCommittee) Map(context.Context, taxonomyapp.Context) (taxonomy.ChangeSet, error) {
	c.mapCalls++
	return testChangeSet(), nil
}

func (c *rejectThenApproveCommittee) Review(context.Context, taxonomyapp.Context) (taxonomy.Review, taxonomy.Review, error) {
	c.reviewCalls++
	if c.reviewCalls == 1 {
		return taxonomy.Review{Decision: taxonomy.ReviewReject, Feedback: "mapping needs a clearer prerequisite"}, taxonomy.Review{Decision: taxonomy.ReviewApprove}, nil
	}
	return taxonomy.Review{Decision: taxonomy.ReviewApprove}, taxonomy.Review{Decision: taxonomy.ReviewApprove}, nil
}

func (c *scriptedCommittee) Map(context.Context, taxonomyapp.Context) (taxonomy.ChangeSet, error) {
	c.mapCalls++
	return testChangeSet(), nil
}

func (c *scriptedCommittee) Review(context.Context, taxonomyapp.Context) (taxonomy.Review, taxonomy.Review, error) {
	c.reviewCalls++
	return taxonomy.Review{Decision: taxonomy.ReviewApprove}, taxonomy.Review{Decision: taxonomy.ReviewApprove}, nil
}

type taxonomyStore struct {
	mu          sync.Mutex
	current     taxonomy.Claim
	claimed     bool
	claimNumber int
	phaseStates []taxonomy.WorkflowState
	roles       []taxonomyapp.AgentRole
	runIDs      []string
	phaseCalls  int
}

func newTaxonomyStore() *taxonomyStore {
	expires := time.Now().UTC().Add(time.Hour)
	return &taxonomyStore{current: taxonomy.Claim{Workflow: taxonomy.Workflow{
		ID: "taxonomy-workflow-test", ChallengeID: "challenge-test", ChallengeRevision: "revision-1",
		State: taxonomy.WorkflowQueued, BaseTaxonomyRevision: "", NextRunAt: time.Now().UTC(),
		LeaseExpiresAt: &expires,
	}, LeaseCredential: taxonomy.LeaseCredential{StateAttempt: 0, LeaseOwner: ""}}}
}

func newTestTaxonomyWorker(t *testing.T, store *taxonomyStore, committee Committee) *Worker {
	t.Helper()
	worker, err := NewWithCommittee(store, committee, config.AgentConfig{Model: "test-model"}, Config{
		WorkerID: "taxonomy-test", LeaseTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func (s *taxonomyStore) Claim(context.Context, string, time.Duration) (*taxonomy.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current.Workflow.State.Terminal() || (s.claimed && s.current.LeaseOwner != "") {
		return nil, nil
	}
	s.claimed = true
	s.claimNumber++
	if s.current.Workflow.State == taxonomy.WorkflowQueued {
		s.current.Workflow.State = taxonomy.WorkflowMapping
	}
	owner := fmt.Sprintf("taxonomy-test-lease-%d", s.claimNumber)
	expires := time.Now().UTC().Add(time.Minute)
	s.current.Workflow.LeaseOwner = owner
	s.current.Workflow.LeaseExpiresAt = &expires
	s.current.LeaseOwner = owner
	s.current.StateAttempt = s.current.Workflow.StateAttempt
	return s.claimLocked(), nil
}

func (s *taxonomyStore) Renew(context.Context, taxonomy.Claim, time.Duration) error { return nil }

func (s *taxonomyStore) Context(_ context.Context, claim taxonomy.Claim) (*taxonomyapp.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if claim.Workflow.ID != s.current.Workflow.ID || claim.Workflow.State != s.current.Workflow.State {
		return nil, taxonomy.ErrLeaseLost
	}
	return &taxonomyapp.Context{
		Workflow:         claim.Workflow,
		Mapper:           taxonomyapp.ModelInput{SystemPrompt: "mapper", Prompt: "map this challenge"},
		CurriculumReview: taxonomyapp.ModelInput{SystemPrompt: "curriculum", Prompt: "review this mapping"},
		SREReview:        taxonomyapp.ModelInput{SystemPrompt: "sre", Prompt: "review this mapping"},
	}, nil
}

func (s *taxonomyStore) StartAgentRun(_ context.Context, claim taxonomy.Claim, role taxonomyapp.AgentRole, _ string) (*taxonomyapp.StartAgentRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if claim.Workflow.State != s.current.Workflow.State || claim.LeaseOwner != s.current.LeaseOwner {
		return nil, taxonomy.ErrLeaseLost
	}
	s.roles = append(s.roles, role)
	runID := fmt.Sprintf("taxonomy-run-%d", len(s.runIDs)+1)
	s.runIDs = append(s.runIDs, runID)
	return &taxonomyapp.StartAgentRunResponse{Run: agent.Run{
		ID: runID, Status: agent.RunRunning, Purpose: role.Purpose(), OwnerKind: "taxonomy-workflow", OwnerRef: claim.Workflow.ID,
	}}, nil
}

func (s *taxonomyStore) Phase(_ context.Context, claim taxonomy.Claim, request taxonomyapp.PhaseRequest) (*taxonomy.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if claim.Workflow.State != s.current.Workflow.State || claim.LeaseOwner != s.current.LeaseOwner {
		return nil, taxonomy.ErrLeaseLost
	}
	s.phaseCalls++
	s.phaseStates = append(s.phaseStates, claim.Workflow.State)
	s.current.Workflow.LastError = ""
	switch {
	case request.Mapper != nil:
		s.current.Workflow.CandidateChangeSet = &request.Mapper.ChangeSet
		s.current.Workflow.State = taxonomy.WorkflowReviewing
		s.current.Workflow.StateAttempt = 0
	case request.ReviewPair != nil:
		s.current.Workflow.CurriculumReview = &request.ReviewPair.Curriculum
		s.current.Workflow.SREReview = &request.ReviewPair.SRE
		if request.ReviewPair.Curriculum.Decision == taxonomy.ReviewReject || request.ReviewPair.SRE.Decision == taxonomy.ReviewReject {
			s.current.Workflow.State = taxonomy.WorkflowMapping
			s.current.Workflow.Round++
			s.current.Workflow.LeaseOwner = ""
			s.current.Workflow.LeaseExpiresAt = nil
			s.current.LeaseOwner = ""
			return nil, nil
		}
		s.current.Workflow.State = taxonomy.WorkflowPublishing
		s.current.Workflow.StateAttempt = 0
	case request.Publication != nil:
		s.current.Workflow.State = taxonomy.WorkflowCompleted
		s.current.Workflow.StateAttempt = 0
		s.current.Workflow.LeaseOwner = ""
		s.current.Workflow.LeaseExpiresAt = nil
		s.current.LeaseOwner = ""
		return nil, nil
	}
	return s.claimLocked(), nil
}

func (s *taxonomyStore) claimLocked() *taxonomy.Claim {
	claim := s.current
	claim.Workflow.LeaseOwner = s.current.Workflow.LeaseOwner
	claim.StateAttempt = s.current.Workflow.StateAttempt
	claim.LeaseOwner = s.current.Workflow.LeaseOwner
	return &claim
}

func testChangeSet() taxonomy.ChangeSet {
	return taxonomy.ChangeSet{ChallengeMappings: []taxonomy.ChallengeMappingChange{{
		Operation:   taxonomy.ChangeUpsert,
		ChallengeID: "challenge-test",
		Value:       &taxonomy.ChallengeMapping{Challenge: taxonomy.ChallengeRef{ID: "challenge-test", Title: "Test challenge", Revision: "revision-1"}},
	}}}
}

func workflowStatesEqual(left, right []taxonomy.WorkflowState) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func rolesEqual(left, right []taxonomyapp.AgentRole) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
