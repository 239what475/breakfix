package taxonomy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"log/slog"
)

const (
	defaultReconcilerCount = 3
	workLeaseTTL           = 3 * time.Minute
	agentCallTimeout       = 10 * time.Minute
	publisherLeaseTTL      = 2 * time.Minute
	publisherLeaseName     = "taxonomy-publisher"

	maxTechnicalFailures = 10
	retryInitialDelay    = time.Minute
	retryMaximumDelay    = time.Hour

	mapperPromptVersion = "taxonomy-mapper-v2"
	reviewPromptVersion = "taxonomy-review-v2"
)

// WorkRepository contains Server-owned taxonomy state. Agent Workers only use
// agentruntime.Repository and the authenticated internal API; they never hold
// this repository or the taxonomy filesystem.
type WorkRepository interface {
	EnqueueTaxonomyWork(context.Context, WorkItem) (*WorkItem, error)
	GetTaxonomyWork(context.Context, string) (*WorkItem, error)
	ClaimTaxonomyWork(context.Context, string, time.Duration) (*WorkItem, error)
	ExtendTaxonomyWorkLease(context.Context, string, string, time.Duration) error
	SaveClaimedTaxonomyWork(context.Context, WorkItem) error
	CancelClaimedTaxonomyWork(context.Context, WorkItem, string) error
	ScheduleTaxonomyRun(context.Context, WorkItem, agentruntime.CreateRun) (*agentruntime.Run, error)
	FinalizeTaxonomyMapperRun(context.Context, agentruntime.Claim, string, string, ChangeSet) error
	FinalizeTaxonomyReviewRun(context.Context, agentruntime.Claim, string, Review, Review) error
	AcquireTaxonomyLease(context.Context, string, string, time.Duration) (bool, error)
	ReleaseTaxonomyLease(context.Context, string, string) error
	GetRun(context.Context, string) (*agentruntime.Run, error)
}

type Service struct {
	repo          WorkRepository
	store         *Store
	challengesDir string
	model         string
	instanceID    string
}

func NewService(repo WorkRepository, store *Store, challengesDir string, llm config.AgentConfig) *Service {
	return &Service{
		repo:          repo,
		store:         store,
		challengesDir: strings.TrimSpace(challengesDir),
		model:         strings.TrimSpace(llm.Model),
		instanceID:    agentruntime.NewID("taxonomy-server"),
	}
}

// Start runs only Server-side scheduling, artifact checks, and publication.
// Eino calls run exclusively in Agent Worker deployments through agent_runs.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.repo == nil || s.store == nil || s.challengesDir == "" || s.model == "" {
		return
	}
	go s.scanLoop(ctx)
	for index := 0; index < defaultReconcilerCount; index++ {
		go s.reconcileLoop(ctx, index)
	}
}

func (s *Service) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.EnqueueUnmapped(ctx); err != nil {
			slog.Warn("scan taxonomy mappings", "error_class", taxonomyErrorClass(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) reconcileLoop(ctx context.Context, index int) {
	for {
		processed, err := s.ProcessOne(ctx, fmt.Sprintf("%s-%d", s.instanceID, index))
		if err != nil {
			slog.Warn("reconcile taxonomy work", "worker", index, "error_class", taxonomyErrorClass(err))
		}
		wait := time.Second
		if processed {
			wait = 10 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// EnqueueUnmapped discovers verified challenge artifacts that cannot yet be
// found in the current taxonomy index. It never writes taxonomy itself.
func (s *Service) EnqueueUnmapped(ctx context.Context) error {
	entries, err := challenge.List(s.challengesDir)
	if err != nil {
		return fmt.Errorf("list challenges for taxonomy: %w", err)
	}
	var snapshot Snapshot
	current, err := s.store.LoadCurrent()
	if err != nil && !errors.Is(err, ErrNoCurrentRevision) {
		return err
	}
	if current != nil {
		snapshot = *current
	}
	index, err := NewCatalogIndex(snapshot, entries)
	if err != nil {
		return fmt.Errorf("index current taxonomy: %w", err)
	}
	for _, entry := range entries {
		if _, mapped := index.Mapping(entry.ID); mapped {
			continue
		}
		if _, err := s.EnqueueChallenge(ctx, entry, snapshot.Revision); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) EnqueueChallenge(ctx context.Context, entry challenge.Entry, baseRevision string) (*WorkItem, error) {
	if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Revision) == "" {
		return nil, errors.New("taxonomy mapping requires a published challenge revision")
	}
	item, err := s.repo.EnqueueTaxonomyWork(ctx, WorkItem{
		ID:                agentruntime.NewID("taxonomy-work"),
		Kind:              WorkKindMapping,
		ChallengeID:       entry.ID,
		ChallengeRevision: entry.Revision,
		BaseRevision:      baseRevision,
		State:             WorkPending,
	})
	if err == nil {
		slog.Info("taxonomy mapping enqueued", "work", item.ID, "challenge", item.ChallengeID, "revision", item.ChallengeRevision)
	}
	return item, err
}

// ProcessOne advances one Server-side scheduling or publishing step. It never
// waits for a model response while holding a WorkItem lease.
func (s *Service) ProcessOne(ctx context.Context, workerID string) (bool, error) {
	item, err := s.repo.ClaimTaxonomyWork(ctx, workerID, workLeaseTTL)
	if err != nil {
		return false, err
	}
	if item == nil {
		return false, nil
	}
	stopHeartbeat := s.keepWorkLeaseAlive(ctx, item.ID, item.LeaseOwner)
	defer stopHeartbeat()
	if item.Kind != WorkKindMapping {
		item.State = WorkFailed
		item.LastError = fmt.Sprintf("unsupported taxonomy work kind %q", item.Kind)
		return true, s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	}
	entry, err := challenge.Get(s.challengesDir, item.ChallengeID)
	if err != nil || entry.Revision != item.ChallengeRevision {
		return true, s.repo.CancelClaimedTaxonomyWork(ctx, *item, "目标 challenge artifact 已不存在或 revision 已变化")
	}
	if item.ActiveRunID != "" {
		return true, s.observeRun(ctx, item)
	}
	if item.State == WorkReadyPublish {
		return true, s.publish(ctx, item, workerID)
	}
	return true, s.scheduleNextStage(ctx, item)
}

func (s *Service) observeRun(ctx context.Context, item *WorkItem) error {
	run, err := s.repo.GetRun(ctx, item.ActiveRunID)
	if err != nil {
		item.ActiveStage = ""
		item.ActiveRunID = ""
		return s.retryTechnical(ctx, item, fmt.Errorf("load active taxonomy run: %w", err))
	}
	switch run.Status {
	case agentruntime.RunPending, agentruntime.RunRunning:
		return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	case agentruntime.RunFailed, agentruntime.RunCancelled:
		item.ActiveStage = ""
		item.ActiveRunID = ""
		message := strings.TrimSpace(run.LastError)
		if message == "" {
			message = "taxonomy agent run ended without a result"
		}
		return s.retryTechnical(ctx, item, errors.New(message))
	case agentruntime.RunSucceeded:
		// Server finalization clears active_run_id atomically with completion. A
		// completed Run still referenced by a WorkItem is an invariant breach.
		item.ActiveStage = ""
		item.ActiveRunID = ""
		return s.retryTechnical(ctx, item, errors.New("taxonomy run succeeded without finalizing its domain stage"))
	default:
		item.ActiveStage = ""
		item.ActiveRunID = ""
		return s.retryTechnical(ctx, item, fmt.Errorf("taxonomy run has unknown status %q", run.Status))
	}
}

func (s *Service) scheduleNextStage(ctx context.Context, item *WorkItem) error {
	if item.hasPartialReviews() {
		// This cannot be created by the new finalizer. Clear old/incomplete
		// state before the pair is rerun instead of treating one conclusion as
		// an official committee result.
		item.CurriculumReview = nil
		item.SREReview = nil
		return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	}
	stage := WorkStageReview
	baseRevision := item.BaseRevision
	if item.needsMapper() {
		current, err := s.currentSnapshot()
		if err != nil {
			return s.retryTechnical(ctx, item, fmt.Errorf("load current taxonomy: %w", err))
		}
		stage = WorkStageMapper
		baseRevision = current.Revision
	}
	if stage == WorkStageReview {
		if item.Candidate == nil {
			return s.retryTechnical(ctx, item, errors.New("taxonomy review stage has no mapper candidate"))
		}
		if _, err := s.snapshotForRevision(baseRevision); err != nil {
			return s.retryTechnical(ctx, item, fmt.Errorf("load candidate taxonomy revision: %w", err))
		}
	}
	item.BaseRevision = baseRevision
	purpose, err := PurposeForStage(stage)
	if err != nil {
		return err
	}
	promptVersion := mapperPromptVersion
	if stage == WorkStageReview {
		promptVersion = reviewPromptVersion
	}
	input, err := json.Marshal(RunInput{WorkID: item.ID, Stage: stage, Round: item.Round})
	if err != nil {
		return fmt.Errorf("encode taxonomy run input: %w", err)
	}
	run, err := s.repo.ScheduleTaxonomyRun(ctx, *item, agentruntime.CreateRun{
		ID:            agentruntime.NewID("taxonomy-run"),
		Purpose:       purpose,
		OwnerKind:     "taxonomy-work",
		OwnerRef:      item.ID,
		InputRevision: baseRevision,
		Input:         input,
		Model:         s.model,
		PromptVersion: promptVersion,
		DeadlineAt:    time.Now().UTC().Add(agentCallTimeout),
	})
	if err != nil {
		return fmt.Errorf("schedule taxonomy %s run: %w", stage, err)
	}
	slog.Info("taxonomy agent run scheduled", "work", item.ID, "run", run.ID, "stage", stage, "round", item.Round)
	return nil
}

func (item WorkItem) needsMapper() bool {
	return item.Candidate == nil || (item.CurriculumReview != nil && item.SREReview != nil)
}

func (item WorkItem) hasPartialReviews() bool {
	return item.Candidate != nil && (item.CurriculumReview == nil) != (item.SREReview == nil)
}

func (s *Service) publish(ctx context.Context, item *WorkItem, workerID string) error {
	if item.Candidate == nil {
		item.State = WorkPending
		item.CurriculumReview = nil
		item.SREReview = nil
		item.LastError = "已批准的 taxonomy work 缺少候选 ChangeSet，重新执行 Mapper"
		return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	}
	acquired, err := s.repo.AcquireTaxonomyLease(ctx, publisherLeaseName, workerID, publisherLeaseTTL)
	if err != nil {
		return err
	}
	if !acquired {
		return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	}
	defer func() {
		if err := s.repo.ReleaseTaxonomyLease(context.Background(), publisherLeaseName, workerID); err != nil {
			slog.Warn("release taxonomy publisher lease", "error_class", taxonomyErrorClass(err))
		}
	}()

	entry, err := challenge.Get(s.challengesDir, item.ChallengeID)
	if err != nil || entry.Revision != item.ChallengeRevision {
		return s.repo.CancelClaimedTaxonomyWork(ctx, *item, "目标 challenge artifact 已不存在或 revision 已变化")
	}
	current, err := s.currentSnapshot()
	if err != nil {
		return s.retryTechnical(ctx, item, fmt.Errorf("load current taxonomy: %w", err))
	}
	base, err := s.snapshotForRevision(item.BaseRevision)
	if err != nil {
		return s.resetForLatest(ctx, item, current.Revision, "候选的 base taxonomy revision 已不可读取")
	}
	if current.Revision != item.BaseRevision {
		if !item.Candidate.MappingOnlyFor(item.ChallengeID) || !referencedDefinitionsUnchanged(base, current, *item.Candidate) {
			return s.resetForLatest(ctx, item, current.Revision, "taxonomy 已变化，需要基于最新 revision 重新进行完整审查")
		}
	}
	next, err := ApplyChangeSet(current, *item.Candidate)
	if err != nil {
		return s.resetForLatest(ctx, item, current.Revision, "候选无法在最新 taxonomy 上确定性重放："+err.Error())
	}
	published, err := s.store.Publish(next)
	if err != nil {
		return s.retryTechnical(ctx, item, fmt.Errorf("publish taxonomy snapshot: %w", err))
	}
	item.BaseRevision = current.Revision
	item.PublishedRevision = published.Revision
	item.State = WorkPublished
	item.LastError = ""
	item.TechnicalFailures = 0
	item.ExecutionFailures = 0
	item.NextRunAt = time.Time{}
	slog.Info("taxonomy revision published", "work", item.ID, "challenge", item.ChallengeID, "taxonomy_revision", published.Revision)
	return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
}

func (s *Service) resetForLatest(ctx context.Context, item *WorkItem, revision, reason string) error {
	item.BaseRevision = revision
	item.Candidate = nil
	item.CurriculumReview = nil
	item.SREReview = nil
	item.ActiveStage = ""
	item.ActiveRunID = ""
	item.State = WorkPending
	item.LastError = reason
	item.TechnicalFailures = 0
	item.ExecutionFailures = 0
	item.NextRunAt = time.Time{}
	return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
}

func (s *Service) retryTechnical(ctx context.Context, item *WorkItem, err error) error {
	item.TechnicalFailures++
	item.LastError = strings.TrimSpace(err.Error())
	if item.LastError == "" {
		item.LastError = "taxonomy work encountered an unspecified technical failure"
	}
	item.ActiveStage = ""
	item.ActiveRunID = ""
	if item.State != WorkReadyPublish {
		item.State = WorkPending
	}
	item.NextRunAt = time.Time{}
	if item.TechnicalFailures >= maxTechnicalFailures {
		item.TechnicalFailures = 0
		item.ExecutionFailures++
		item.NextRunAt = time.Now().UTC().Add(retryDelay(item.ExecutionFailures))
		slog.Warn("taxonomy execution failure budget exhausted", "work", item.ID, "challenge", item.ChallengeID, "round", item.Round, "execution_failures", item.ExecutionFailures, "error_class", taxonomyErrorClass(err))
	} else {
		slog.Warn("taxonomy technical failure", "work", item.ID, "challenge", item.ChallengeID, "round", item.Round, "technical_failures", item.TechnicalFailures, "error_class", taxonomyErrorClass(err))
	}
	return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
}

func retryDelay(executionFailures int) time.Duration {
	if executionFailures <= 1 {
		return retryInitialDelay
	}
	delay := retryInitialDelay
	for attempt := 1; attempt < executionFailures && delay < retryMaximumDelay; attempt++ {
		delay *= 2
		if delay >= retryMaximumDelay {
			return retryMaximumDelay
		}
	}
	return delay
}

func (s *Service) keepWorkLeaseAlive(ctx context.Context, workID, owner string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(workLeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.repo.ExtendTaxonomyWorkLease(context.Background(), workID, owner, workLeaseTTL); err != nil {
					slog.Warn("extend taxonomy work lease", "work", workID, "error_class", taxonomyErrorClass(err))
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func (s *Service) currentSnapshot() (Snapshot, error) {
	snapshot, err := s.store.LoadCurrent()
	if errors.Is(err, ErrNoCurrentRevision) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	return *snapshot, nil
}

func (s *Service) snapshotForRevision(revision string) (Snapshot, error) {
	if revision == "" {
		return Snapshot{}, nil
	}
	snapshot, err := s.store.LoadRevision(revision)
	if err != nil {
		return Snapshot{}, err
	}
	return *snapshot, nil
}

// ExecutionContext is reconstructed by the Server for one claimed Agent Run.
// It contains model input but no credentials, database handle, or filesystem
// path. The Worker sends only a typed result back through the internal API.
type ExecutionContext struct {
	Stage        WorkStage `json:"stage"`
	WorkID       string    `json:"work_id"`
	SystemPrompt string    `json:"system_prompt"`
	Prompt       string    `json:"prompt"`
}

func (s *Service) LoadExecutionContext(ctx context.Context, claim agentruntime.Claim) (ExecutionContext, error) {
	item, input, entry, base, artifact, err := s.contextForClaim(ctx, claim)
	if err != nil {
		return ExecutionContext{}, err
	}
	switch input.Stage {
	case WorkStageMapper:
		baseJSON, err := json.MarshalIndent(base, "", "  ")
		if err != nil {
			return ExecutionContext{}, err
		}
		prior, err := mapperPriorContext(item)
		if err != nil {
			return ExecutionContext{}, err
		}
		return ExecutionContext{Stage: input.Stage, WorkID: item.ID, SystemPrompt: mapperSystemPrompt, Prompt: fmt.Sprintf(`已验证 challenge：
- id: %s
- title: %s
- revision: %s

当前 taxonomy snapshot：
%s

challenge 完整 artifact：
%s
%s
请调用 submit_changeset 提交一个完整 ChangeSet。它必须只映射这个 challenge；可以新增或修改 Skill、Tag 和 Skill.requires，但不得修改其他 challenge mapping。`, entry.ID, entry.Title, entry.Revision, baseJSON, artifact, prior)}, nil
	case WorkStageReview:
		if item.Candidate == nil {
			return ExecutionContext{}, errors.New("taxonomy review stage has no mapper candidate")
		}
		baseJSON, err := json.MarshalIndent(base, "", "  ")
		if err != nil {
			return ExecutionContext{}, err
		}
		changesJSON, err := json.MarshalIndent(item.Candidate, "", "  ")
		if err != nil {
			return ExecutionContext{}, err
		}
		return ExecutionContext{Stage: input.Stage, WorkID: item.ID, SystemPrompt: reviewerSystemPrompt, Prompt: fmt.Sprintf(`已验证 challenge：%s (%s)

当前 taxonomy：
%s

候选 ChangeSet：
%s

challenge artifact：
%s`, entry.Title, entry.Revision, baseJSON, changesJSON, artifact)}, nil
	default:
		return ExecutionContext{}, errors.New("taxonomy run has an unknown stage")
	}
}

func (s *Service) FinalizeMapper(ctx context.Context, claim agentruntime.Claim, changes ChangeSet) error {
	item, input, entry, base, _, err := s.contextForClaim(ctx, claim)
	if err != nil {
		return err
	}
	if input.Stage != WorkStageMapper {
		return errors.New("taxonomy run is not a mapper stage")
	}
	if _, err := validateMappingChangeSet(changes, entry, base); err != nil {
		return fmt.Errorf("mapper candidate failed deterministic validation: %w", err)
	}
	return s.repo.FinalizeTaxonomyMapperRun(ctx, claim, item.ID, base.Revision, changes)
}

func (s *Service) FinalizeReviewPair(ctx context.Context, claim agentruntime.Claim, curriculum, sre Review) error {
	item, input, _, _, _, err := s.contextForClaim(ctx, claim)
	if err != nil {
		return err
	}
	if input.Stage != WorkStageReview {
		return errors.New("taxonomy run is not a review stage")
	}
	if err := ValidateReview(curriculum); err != nil {
		return fmt.Errorf("invalid curriculum review: %w", err)
	}
	if err := ValidateReview(sre); err != nil {
		return fmt.Errorf("invalid SRE review: %w", err)
	}
	return s.repo.FinalizeTaxonomyReviewRun(ctx, claim, item.ID, curriculum, sre)
}

func (s *Service) contextForClaim(ctx context.Context, claim agentruntime.Claim) (*WorkItem, RunInput, challenge.Entry, Snapshot, string, error) {
	if !claim.Valid() {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", errors.New("taxonomy execution requires a valid run claim")
	}
	input, err := DecodeRunInput(claim.Run.Input)
	if err != nil {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", fmt.Errorf("decode taxonomy run input: %w", err)
	}
	purpose, err := PurposeForStage(input.Stage)
	if err != nil {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", err
	}
	if claim.Run.Purpose != purpose || claim.Run.OwnerKind != "taxonomy-work" || claim.Run.OwnerRef != input.WorkID {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", errors.New("taxonomy run does not own its work item")
	}
	item, err := s.repo.GetTaxonomyWork(ctx, input.WorkID)
	if err != nil {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", err
	}
	if item.ActiveRunID != claim.Run.ID || item.ActiveStage != input.Stage || item.Round != input.Round {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", errors.New("taxonomy run no longer owns its scheduled stage")
	}
	entry, err := challenge.Get(s.challengesDir, item.ChallengeID)
	if err != nil || entry.Revision != item.ChallengeRevision {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", errors.New("目标 challenge artifact 已不存在或 revision 已变化")
	}
	base, err := s.snapshotForRevision(claim.Run.InputRevision)
	if err != nil {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", fmt.Errorf("load taxonomy base revision: %w", err)
	}
	artifact, err := readArtifact(entry.Dir)
	if err != nil {
		return nil, RunInput{}, challenge.Entry{}, Snapshot{}, "", fmt.Errorf("read challenge artifact: %w", err)
	}
	return item, input, *entry, base, artifact, nil
}

func mapperPriorContext(item *WorkItem) (string, error) {
	if item.Candidate == nil {
		return "", nil
	}
	candidate, err := json.MarshalIndent(item.Candidate, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("\n上一轮候选 ChangeSet：\n%s\n课程审核意见：%s\nSRE 审核意见：%s\n", candidate, formatReview(item.CurriculumReview), formatReview(item.SREReview)), nil
}

func validateMappingChangeSet(changes ChangeSet, entry challenge.Entry, base Snapshot) (Snapshot, error) {
	if len(changes.ChallengeMappings) != 1 {
		return Snapshot{}, errors.New("mapper must emit exactly one challenge mapping change")
	}
	change := changes.ChallengeMappings[0]
	if change.Operation != ChangeUpsert || change.Value == nil {
		return Snapshot{}, errors.New("mapper must upsert the target challenge mapping")
	}
	if change.Value.Challenge.ID != entry.ID || change.Value.Challenge.Title != entry.Title || change.Value.Challenge.Revision != entry.Revision {
		return Snapshot{}, errors.New("mapper challenge mapping must exactly match the verified challenge id, title, and revision")
	}
	return ApplyChangeSet(base, changes)
}

func ValidateReview(review Review) error {
	switch review.Decision {
	case ReviewApprove:
		if strings.TrimSpace(review.Feedback) != "" {
			return errors.New("approve review must not include feedback")
		}
	case ReviewReject:
		if strings.TrimSpace(review.Feedback) == "" {
			return errors.New("reject review must include concrete feedback")
		}
	default:
		return fmt.Errorf("review decision must be approve or reject, got %q", review.Decision)
	}
	return nil
}

func referencedDefinitionsUnchanged(base, current Snapshot, changes ChangeSet) bool {
	if !changes.MappingOnlyFor(changeChallengeID(changes.ChallengeMappings[0])) {
		return false
	}
	mapping := changes.ChallengeMappings[0].Value
	if mapping == nil {
		return false
	}
	baseSkills, currentSkills := skillMap(base), skillMap(current)
	baseTags, currentTags := tagMap(base), tagMap(current)
	for _, ref := range append(append([]Ref{}, mapping.EntrySkills...), outcomeRefs(mapping.Outcomes)...) {
		if !reflect.DeepEqual(baseSkills[ref.ID], currentSkills[ref.ID]) {
			return false
		}
	}
	for _, ref := range mapping.Tags {
		if !reflect.DeepEqual(baseTags[ref.ID], currentTags[ref.ID]) {
			return false
		}
	}
	return true
}

func skillMap(snapshot Snapshot) map[string]Skill {
	result := make(map[string]Skill, len(snapshot.Skills))
	for _, skill := range snapshot.Skills {
		result[skill.ID] = skill
	}
	return result
}

func tagMap(snapshot Snapshot) map[string]Tag {
	result := make(map[string]Tag, len(snapshot.Tags))
	for _, tag := range snapshot.Tags {
		result[tag.ID] = tag
	}
	return result
}

func outcomeRefs(outcomes []OutcomeRef) []Ref {
	result := make([]Ref, 0, len(outcomes))
	for _, outcome := range outcomes {
		result = append(result, Ref{ID: outcome.ID, Title: outcome.Title})
	}
	return result
}

func readArtifact(root string) (string, error) {
	files := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("taxonomy artifact has unsupported file %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return "", err
	}
	slices.Sort(files)
	var builder strings.Builder
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&builder, "\n--- %s ---\n%s\n", rel, data)
	}
	return builder.String(), nil
}

func formatReview(review *Review) string {
	if review == nil {
		return "无"
	}
	data, err := json.Marshal(review)
	if err != nil {
		return "无"
	}
	return string(data)
}

func taxonomyErrorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, agentruntime.ErrLeaseLost) {
		return "lease_lost"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	return "taxonomy"
}

const mapperSystemPrompt = `你是 Breakfix Taxonomy Mapper。你只负责将已经真实验证并发布的 challenge 映射到独立的 Skill、Tag 和关系；不得修改 challenge 本身。

Skill 必须是可独立解释、能在多题复用的能力；Tag 仅用于稳定的 Catalog 浏览维度，不能用 Tag 复述细粒度 Skill。优先复用现有定义；新增 Skill、Tag 或 requires 关系时，必须有明确、可复用的语义理由。只映射当前 challenge，不能修改其他 challenge 的 mapping。

完成分析后必须且只能调用 submit_changeset。`

const reviewerSystemPrompt = `你是 Breakfix Taxonomy Committee Reviewer。你将以两个独立视角同时审查同一份候选 ChangeSet：

1. Curriculum：检查 Skill、Tag、entry/outcome 和 requires 是否清楚、可复用且教学上合理；不要把宽泛领域或单条命令参数伪装成 Skill。
2. SRE：依据已经验证的 challenge artifact，检查候选是否忠实描述实际排障目标、环境和检查点，而不是题意猜测；检查关系是否造成技术误导。

每个视角完成后必须且只能调用自己的结果工具。通过时不能附带 feedback；驳回时 feedback 必须是具体、可执行的中文问题。`
