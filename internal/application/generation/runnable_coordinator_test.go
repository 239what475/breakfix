package generation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appoperations "github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/content/candidate"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestRunnableCoordinatorResumesMaterializationWithStablePublicAction(t *testing.T) {
	workflow, revision := runnableCoordinatorFixture(t, domain.StateMaterializingArtifact, 3)
	store := &runnableCoordinatorStore{workflows: []domain.Workflow{workflow}, candidates: map[string]domain.Revision{workflow.ID: revision}}
	runtime := &runnableCoordinatorRuntime{materialized: runnable.RevisionReference{ID: "runnable-revision-01", Digest: coordinatorDigest("a")}}
	coordinator := newRunnableCoordinatorForTest(t, store, runtime)

	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatalf("schedule pending materialization: %v", err)
	}
	if len(runtime.sources) != 1 || runtime.materializeCalls != 1 || len(store.materialized) != 0 {
		t.Fatalf("pending materialization = sources:%d schedules:%d projections:%d", len(runtime.sources), runtime.materializeCalls, len(store.materialized))
	}
	first := runtime.materializeActions[0]
	if first.Phase != runnable.ActionMaterializeArtifact || first.StateVersion != workflow.StateVersion {
		t.Fatalf("materialization action = %#v", first)
	}

	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("recover pending materialization: %v", err)
	}
	if runtime.materializeCalls != 2 || runtime.materializeActions[1] != first || len(store.materialized) != 0 {
		t.Fatalf("recovered materialization was not idempotent: actions=%#v projections=%#v", runtime.materializeActions, store.materialized)
	}

	runtime.materializationReady = true
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatalf("project materialized revision: %v", err)
	}
	if len(store.materialized) != 1 || store.materialized[0].reference != runtime.materialized {
		t.Fatalf("materialized projection = %#v", store.materialized)
	}
	if got := store.candidates[workflow.ID].RunnableRevisionRef; got == nil || *got != runtime.materialized {
		t.Fatalf("candidate runnable revision = %#v", got)
	}
	if got := store.workflows[0]; got.State != domain.StateVerifying || got.StateVersion != workflow.StateVersion+1 {
		t.Fatalf("workflow after materialization = %#v", got)
	}
}

func TestRunnableCoordinatorResumesVerificationWithStoredRevision(t *testing.T) {
	workflow, revision := runnableCoordinatorFixture(t, domain.StateVerifying, 7)
	storedRevision := runnable.RevisionReference{ID: "runnable-revision-02", Digest: coordinatorDigest("b")}
	revision.RunnableRevisionRef = &storedRevision
	store := &runnableCoordinatorStore{workflows: []domain.Workflow{workflow}, candidates: map[string]domain.Revision{workflow.ID: revision}}
	report := runnable.VerificationReportReference{ID: "verification-report-01", Digest: coordinatorDigest("c")}
	runtime := &runnableCoordinatorRuntime{report: runnable.StoredVerificationReport{Reference: report}}
	coordinator := newRunnableCoordinatorForTest(t, store, runtime)

	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatalf("schedule pending verification: %v", err)
	}
	if runtime.verifyCalls != 1 || len(store.verified) != 0 {
		t.Fatalf("pending verification = schedules:%d projections:%d", runtime.verifyCalls, len(store.verified))
	}
	first := runtime.verifyActions[0]
	if first.Phase != runnable.ActionVerify || first.Content.ID != revision.ID || first.StateVersion != workflow.StateVersion {
		t.Fatalf("verification action = %#v", first)
	}
	if runtime.verifyReferences[0] != storedRevision {
		t.Fatalf("verification did not use stored runnable revision: %#v", runtime.verifyReferences)
	}

	if err := coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("recover pending verification: %v", err)
	}
	if runtime.verifyCalls != 2 || runtime.verifyActions[1] != first || len(store.verified) != 0 {
		t.Fatalf("recovered verification was not idempotent: actions=%#v projections=%#v", runtime.verifyActions, store.verified)
	}

	runtime.verificationReady = true
	if err := coordinator.RunOnce(context.Background()); err != nil {
		t.Fatalf("project verification report: %v", err)
	}
	if len(store.verified) != 1 || store.verified[0].reference != report {
		t.Fatalf("verification projection = %#v", store.verified)
	}
	if got := store.candidates[workflow.ID].VerificationReportRef; got == nil || *got != report {
		t.Fatalf("candidate verification report = %#v", got)
	}
}

// An explicitly failed action takes the workflow to its Failed terminal state
// on the very next pass instead of being observed as "not ready" forever.
func TestRunnableCoordinatorFailsWorkflowForFailedActions(t *testing.T) {
	failure := &runnable.ActionFailure{Class: runnable.FailureInfrastructure, Code: "attempts-exhausted", Summary: "provider kept failing"}

	materializing, materializingRevision := runnableCoordinatorFixture(t, domain.StateMaterializingArtifact, 3)
	materializingStore := &runnableCoordinatorStore{workflows: []domain.Workflow{materializing}, candidates: map[string]domain.Revision{materializing.ID: materializingRevision}}
	materializingRuntime := &runnableCoordinatorRuntime{materializationFailure: failure}
	if err := newRunnableCoordinatorForTest(t, materializingStore, materializingRuntime).RunOnce(context.Background()); err != nil {
		t.Fatalf("fail materializing workflow: %v", err)
	}
	if len(materializingStore.materialized) != 0 {
		t.Fatalf("failed materialization projected = %#v", materializingStore.materialized)
	}
	if got := materializingStore.workflows[0]; got.State != domain.StateFailed || got.LastError == "" || !strings.Contains(got.LastError, "attempts-exhausted") {
		t.Fatalf("failed workflow = %#v, want Failed with the action failure", got)
	}

	verifying, verifyingRevision := runnableCoordinatorFixture(t, domain.StateVerifying, 7)
	storedRevision := runnable.RevisionReference{ID: "runnable-revision-02", Digest: coordinatorDigest("b")}
	verifyingRevision.RunnableRevisionRef = &storedRevision
	verifyingStore := &runnableCoordinatorStore{workflows: []domain.Workflow{verifying}, candidates: map[string]domain.Revision{verifying.ID: verifyingRevision}}
	verifyingRuntime := &runnableCoordinatorRuntime{verificationFailure: failure}
	if err := newRunnableCoordinatorForTest(t, verifyingStore, verifyingRuntime).RunOnce(context.Background()); err != nil {
		t.Fatalf("fail verifying workflow: %v", err)
	}
	if len(verifyingStore.verified) != 0 {
		t.Fatalf("failed verification projected = %#v", verifyingStore.verified)
	}
	if got := verifyingStore.workflows[0]; got.State != domain.StateFailed || !strings.Contains(got.LastError, "provider kept failing") {
		t.Fatalf("failed verifying workflow = %#v, want Failed with the action failure", got)
	}
}

func newRunnableCoordinatorForTest(t *testing.T, store *runnableCoordinatorStore, runtime *runnableCoordinatorRuntime) *RunnableCoordinator {
	t.Helper()
	coordinator, err := NewRunnableCoordinator(store, runtime, runnableCoordinatorOperationsConfig(), time.Second)
	if err != nil {
		t.Fatalf("create runnable coordinator: %v", err)
	}
	coordinator.now = func() time.Time { return time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC) }
	return coordinator
}

func runnableCoordinatorFixture(t *testing.T, state domain.WorkflowState, stateVersion int64) (domain.Workflow, domain.Revision) {
	t.Helper()
	archive := generatorServiceCandidateArchive(t)
	path, digest, err := candidate.SaveArchiveAtomic(t.TempDir(), "candidate-coordinator", archive)
	if err != nil {
		t.Fatalf("save candidate archive: %v", err)
	}
	source, _, contentRevision, err := FreezeCandidateSource(archive)
	if err != nil {
		t.Fatalf("freeze candidate source: %v", err)
	}
	workflow := domain.Workflow{
		ID: "workflow-coordinator", Source: domain.Source{Kind: domain.SourceAuthoring, Ref: "authoring-coordinator"}, SourceRevision: "1",
		State: state, CandidateRevisionID: "candidate-coordinator", StateVersion: stateVersion,
		NextRunAt: time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC), CreatedAt: time.Date(2026, time.September, 16, 11, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC),
	}
	revision := domain.Revision{
		ID: "candidate-coordinator", Source: workflow.Source, SourceRevision: workflow.SourceRevision,
		ArchivePath: path, ArchiveDigest: digest, ContentRevision: contentRevision, SourceArchive: source,
		CreatedAt: workflow.CreatedAt, UpdatedAt: workflow.UpdatedAt,
	}
	return workflow, revision
}

func runnableCoordinatorOperationsConfig() appoperations.Config {
	profile := appoperations.RuntimeProfileConfig{
		ProfileRevision: "profile-01", BaseImage: "registry.example/base@" + coordinatorDigest("d"),
		Resources: runnable.ResourceLimits{CPU: "2", MemoryBytes: 2 << 30, EphemeralBytes: 4 << 30, MaxProcesses: 128, MaxConcurrentTasks: 1},
		Network:   runnable.NetworkPrivate, MaxActionTimeout: 1200,
	}
	return appoperations.Config{
		MaxNodes: 4, Node: profile, K8s: profile,
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 600, ResetTimeoutSeconds: 600, StopTimeoutSeconds: 300, ReapTimeoutSeconds: 300, IdleTTLSeconds: 1800, MaxLifetimeSeconds: 3600},
	}
}

type runnableCoordinatorStore struct {
	workflows    []domain.Workflow
	candidates   map[string]domain.Revision
	materialized []runnableCoordinatorMaterialized
	verified     []runnableCoordinatorVerified
	failures     []runnableCoordinatorFailure
}

type runnableCoordinatorMaterialized struct {
	workflowID string
	reference  runnable.RevisionReference
}

type runnableCoordinatorVerified struct {
	workflowID string
	reference  runnable.VerificationReportReference
}

func (s *runnableCoordinatorStore) ListRunnableGenerationCandidates(context.Context) ([]domain.Workflow, error) {
	return append([]domain.Workflow(nil), s.workflows...), nil
}

func (s *runnableCoordinatorStore) CandidateForWorkflow(_ context.Context, workflowID string) (*domain.Revision, error) {
	value, found := s.candidates[workflowID]
	if !found {
		return nil, errors.New("candidate was not found")
	}
	return &value, nil
}

func (s *runnableCoordinatorStore) MarkGenerationCandidateMaterialized(_ context.Context, workflowID string, reference runnable.RevisionReference, _ time.Time) error {
	value, found := s.candidates[workflowID]
	if !found {
		return errors.New("candidate was not found")
	}
	value.RunnableRevisionRef = &reference
	s.candidates[workflowID] = value
	for index := range s.workflows {
		if s.workflows[index].ID == workflowID {
			s.workflows[index].State = domain.StateVerifying
			s.workflows[index].StateVersion++
		}
	}
	s.materialized = append(s.materialized, runnableCoordinatorMaterialized{workflowID: workflowID, reference: reference})
	return nil
}

func (s *runnableCoordinatorStore) MarkGenerationCandidateVerified(_ context.Context, workflowID string, reference runnable.VerificationReportReference, _ time.Time) error {
	value, found := s.candidates[workflowID]
	if !found {
		return errors.New("candidate was not found")
	}
	value.VerificationReportRef = &reference
	s.candidates[workflowID] = value
	s.verified = append(s.verified, runnableCoordinatorVerified{workflowID: workflowID, reference: reference})
	return nil
}

func (s *runnableCoordinatorStore) FailRunnableGenerationWorkflow(_ context.Context, workflowID, message string, _ time.Time) error {
	for index := range s.workflows {
		if s.workflows[index].ID == workflowID {
			s.workflows[index].State = domain.StateFailed
			s.workflows[index].StateVersion++
			s.workflows[index].LastError = message
		}
	}
	s.failures = append(s.failures, runnableCoordinatorFailure{workflowID: workflowID, message: message})
	return nil
}

type runnableCoordinatorFailure struct {
	workflowID string
	message    string
}

type runnableCoordinatorRuntime struct {
	sources              [][]byte
	materialized         runnable.RevisionReference
	report               runnable.StoredVerificationReport
	materializationReady bool
	verificationReady    bool
	materializeCalls     int
	verifyCalls          int
	materializeActions   []runnable.ActionIdentity
	verifyActions        []runnable.ActionIdentity
	verifyReferences     []runnable.RevisionReference
	// Explicit terminal failures, as the Postgres resolvers raise them for a
	// failed action row.
	materializationFailure *runnable.ActionFailure
	verificationFailure    *runnable.ActionFailure
}

func (r *runnableCoordinatorRuntime) StoreRunnableSource(_ context.Context, source runnable.SourceArchive, archive []byte, _ time.Time) error {
	if source.Validate() != nil || !strings.HasPrefix(source.Reference, "runnable-source://sha256/") {
		return errors.New("invalid public source")
	}
	r.sources = append(r.sources, append([]byte(nil), archive...))
	return nil
}

func (r *runnableCoordinatorRuntime) ScheduleMaterialization(_ context.Context, spec runnable.RunnableSpec, stateVersion int64, _ time.Time) (runnable.ActionIdentity, error) {
	digest, err := spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	action := runnable.ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: runnable.ActionMaterializeArtifact, StateVersion: stateVersion}
	r.materializeCalls++
	r.materializeActions = append(r.materializeActions, action)
	return action, nil
}

func (r *runnableCoordinatorRuntime) ResolveMaterializedRunnableRevision(_ context.Context, action runnable.ActionIdentity) (runnable.RevisionReference, error) {
	if r.materializationFailure != nil {
		return runnable.RevisionReference{}, r.materializationFailure
	}
	if !r.materializationReady {
		return runnable.RevisionReference{}, runnable.ErrMaterializationNotReady
	}
	if len(r.materializeActions) == 0 || action != r.materializeActions[len(r.materializeActions)-1] {
		return runnable.RevisionReference{}, errors.New("unknown materialization action")
	}
	return r.materialized, nil
}

func (r *runnableCoordinatorRuntime) ScheduleVerification(_ context.Context, reference runnable.RevisionReference, stateVersion int64, _ time.Time) (runnable.ActionIdentity, error) {
	if err := reference.Validate(); err != nil {
		return runnable.ActionIdentity{}, err
	}
	action := runnable.ActionIdentity{Content: runnable.ContentIdentity{Kind: "operations", ID: "candidate-coordinator", Revision: "revision-01"}, SpecDigest: coordinatorDigest("f"), Phase: runnable.ActionVerify, StateVersion: stateVersion}
	r.verifyCalls++
	r.verifyActions = append(r.verifyActions, action)
	r.verifyReferences = append(r.verifyReferences, reference)
	return action, nil
}

func (r *runnableCoordinatorRuntime) ResolveVerificationForAction(_ context.Context, action runnable.ActionIdentity) (runnable.StoredVerificationReport, error) {
	if r.verificationFailure != nil {
		return runnable.StoredVerificationReport{}, r.verificationFailure
	}
	if !r.verificationReady {
		return runnable.StoredVerificationReport{}, runnable.ErrMaterializationNotReady
	}
	if len(r.verifyActions) == 0 || action != r.verifyActions[len(r.verifyActions)-1] {
		return runnable.StoredVerificationReport{}, errors.New("unknown verification action")
	}
	return r.report, nil
}

func coordinatorDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
