package documentpractice

import (
	"context"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestAgentPipelineRunsIndependentRolesAndPublishes(t *testing.T) {
	now := time.Date(2026, 9, 15, 17, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.CreatedAt = now
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	seed := serviceCandidate(t, plan, []byte("seed archive"), now)
	blueprint := pipelineBlueprint(plan, seed)
	compiled, _, err := CompileCandidate(plan, seed.Spec.RuntimeProfile, seed.Spec.LifecyclePolicy, blueprint, now)
	if err != nil {
		t.Fatal(err)
	}
	revision, report := serviceRevisionAndReport(t, compiled, now, true)
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{revision: revision, report: report})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{page: page, metadata: domain.Metadata{Context: plan.Context, Path: page.Path, Title: plan.Title, Anchors: []string{"pod-lifecycle"}}}, fakePlanAgent{plan: plan}, []PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}}, pipelineGenerator{blueprint: blueprint}, []CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{profile: seed.Spec.RuntimeProfile}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newRunID = sequentialRunIDs()

	started, err := pipeline.Start(context.Background(), "agent-pipeline-full", page.Path, "")
	if err != nil || started.Workflow.State != domain.MaterializingArtifact || started.MaterializationAction.Phase != runnable.ActionMaterializeArtifact {
		t.Fatalf("start pipeline = %#v, %v", started, err)
	}
	if err := pipeline.ReconcileCompletedAction(context.Background(), started.MaterializationAction); err != nil {
		t.Fatalf("reconcile materialization = %v", err)
	}
	workflow, err := service.store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil || workflow.State != domain.Verifying || !store.actions[started.MaterializationAction.Key()].reconciled {
		t.Fatalf("materialization workflow = %#v, binding = %#v, %v", workflow, store.actions[started.MaterializationAction.Key()], err)
	}
	var verification runnable.ActionIdentity
	for _, action := range store.actions {
		if action.action.Phase == runnable.ActionVerify {
			verification = action.action
		}
	}
	if verification == (runnable.ActionIdentity{}) {
		t.Fatal("verification action was not durably bound")
	}
	if err := pipeline.ReconcileCompletedAction(context.Background(), verification); err != nil {
		t.Fatalf("reconcile verification = %v", err)
	}
	workflow, err = service.store.GetWorkflow(context.Background(), started.Workflow.ID)
	if err != nil || workflow.State != domain.Published || store.published == nil || !store.actions[verification.Key()].reconciled {
		t.Fatalf("verification workflow = %#v, binding = %#v, %v", workflow, store.actions[verification.Key()], err)
	}
	if len(store.audits) != 7 { // planner, two plan reviews, generator, two artifact reviews, verification review.
		t.Fatalf("Agent audits = %d, want 7", len(store.audits))
	}
}

func TestAgentPipelineEndsApprovedNoPracticeWithoutGeneration(t *testing.T) {
	now := time.Date(2026, 9, 15, 17, 0, 0, 0, time.UTC)
	plan := validPlan()
	plan.NoPractice = true
	plan.CreatedAt = now
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	store := newMemoryDocumentStore()
	service, err := NewService(store, &memoryRunnableStore{})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	pipeline, err := NewAgentPipeline(service, fakePlannerReader{page: page, metadata: domain.Metadata{Context: plan.Context, Path: page.Path, Title: plan.Title}}, fakePlanAgent{plan: plan}, []PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}}, pipelineGenerator{fail: true}, []CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = func() time.Time { return now }
	pipeline.newRunID = sequentialRunIDs()
	got, err := pipeline.Start(context.Background(), "agent-pipeline-no-practice", page.Path, "")
	if err != nil || got.Workflow.State != domain.NoPractice || got.MaterializationAction != (runnable.ActionIdentity{}) {
		t.Fatalf("no-practice pipeline = %#v, %v", got, err)
	}
}

type pipelineProfiles struct{ profile runnable.RuntimeProfile }

func (p pipelineProfiles) DocumentationRuntimeConstraints() []domain.RuntimeConstraint {
	return []domain.RuntimeConstraint{{Runtime: p.profile.Runtime, BaseImage: p.profile.BaseImage, Resources: p.profile.Resources, Network: p.profile.Network, Topology: p.profile.Topology}}
}

func (p pipelineProfiles) DocumentationLifecyclePolicy() runnable.LifecyclePolicy {
	return runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 300, MaxLifetimeSeconds: 600}
}

func (p pipelineProfiles) ResolveDocumentationRuntimeProfile(domain.RuntimeConstraint) (runnable.RuntimeProfile, error) {
	return p.profile, nil
}

type pipelineGenerator struct {
	blueprint CandidateBlueprint
	fail      bool
}

func (g pipelineGenerator) Generate(context.Context, domain.LearningUnitPlan, runnable.RuntimeProfile) (CandidateBlueprint, error) {
	if g.fail {
		return CandidateBlueprint{}, errPipelineGeneratorCalled
	}
	return g.blueprint, nil
}

type pipelineReviewer struct{ role string }

func (r pipelineReviewer) ReviewPlan(_ context.Context, runID string, _ domain.LearningUnitPlan) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}, nil
}

func (r pipelineReviewer) ReviewCandidate(_ context.Context, runID string, _ domain.LearningUnitPlan, _ domain.PracticeCandidate, _ []GeneratedFile) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}, nil
}

func (r pipelineReviewer) ReviewVerification(_ context.Context, runID string, _ domain.LearningUnitPlan, _ runnable.VerificationReport) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{ReviewerID: runID, Role: r.role, Decision: domain.ReviewApprove, PolicyVersion: "policy-v1"}, nil
}

func pipelineBlueprint(plan domain.LearningUnitPlan, seed domain.PracticeCandidate) CandidateBlueprint {
	return CandidateBlueprint{ID: "pod-lifecycle-generated", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, UserSteps: plan.UserSteps, Observations: plan.Observations, Initialization: seed.Spec.Initialization, ValidationPlan: seed.Spec.ValidationPlan, Files: []GeneratedFile{
		{Path: "scripts/init.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/apply.sh", Content: "#!/bin/sh\nexit 0\n", Executable: true},
		{Path: "scripts/assert.sh", Content: "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"pod-running\",\"satisfied\":true,\"summary\":\"running\"}]}'\n", Executable: true},
	}}
}

func sequentialRunIDs() func(string) (string, error) {
	next := 0
	return func(prefix string) (string, error) {
		next++
		return prefix + "-run-" + string(rune('0'+next)), nil
	}
}

var errPipelineGeneratorCalled = &pipelineError{"generator should not run"}

type pipelineError struct{ message string }

func (e *pipelineError) Error() string { return e.message }
