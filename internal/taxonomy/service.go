package taxonomy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	claudecode "github.com/239what475/eino-claude-code"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"log/slog"
)

const (
	defaultWorkerCount = 3
	workLeaseTTL       = 3 * time.Minute
	agentCallTimeout   = 10 * time.Minute
	publisherLeaseTTL  = 2 * time.Minute
	publisherLeaseName = "taxonomy-publisher"

	maxTechnicalFailures = 10
	retryInitialDelay    = time.Minute
	retryMaximumDelay    = time.Hour
)

type WorkRepository interface {
	EnqueueTaxonomyWork(context.Context, WorkItem) (*WorkItem, error)
	ClaimTaxonomyWork(context.Context, string, time.Duration) (*WorkItem, error)
	ExtendTaxonomyWorkLease(context.Context, string, string, time.Duration) error
	MarkTaxonomyWorkAgentStarted(context.Context, string, string, WorkAgent) error
	SaveClaimedTaxonomyWork(context.Context, WorkItem) error
	AcquireTaxonomyLease(context.Context, string, string, time.Duration) (bool, error)
	ReleaseTaxonomyLease(context.Context, string, string) error
}

type AgentRequest struct {
	Role         string
	SessionID    string
	Resume       bool
	SystemPrompt string
	Prompt       string
	OutputSchema string
}

// AgentRunner is intentionally small: agents return strict JSON only, while
// snapshot mutation and publication remain model-free and testable.
type AgentRunner interface {
	Run(context.Context, AgentRequest) (string, error)
}

type Service struct {
	repo          WorkRepository
	store         *Store
	challengesDir string
	agent         AgentRunner
	instanceID    string
}

func NewService(repo WorkRepository, store *Store, challengesDir string, llm config.AgentConfig) *Service {
	return NewServiceWithRunner(repo, store, challengesDir, &claudeRunner{llm: llm})
}

func NewServiceWithRunner(repo WorkRepository, store *Store, challengesDir string, runner AgentRunner) *Service {
	return &Service{repo: repo, store: store, challengesDir: challengesDir, agent: runner, instanceID: newWorkID("taxonomy-worker")}
}

// Start runs a durable scanner and multiple fair workers. All cross-process
// coordination is delegated to DB leases, so Server instances may scale out.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.repo == nil || s.store == nil || s.agent == nil || strings.TrimSpace(s.challengesDir) == "" {
		return
	}
	go s.scanLoop(ctx)
	for index := 0; index < defaultWorkerCount; index++ {
		go s.workerLoop(ctx, index)
	}
}

func (s *Service) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.EnqueueUnmapped(ctx); err != nil {
			slog.Warn("scan taxonomy mappings", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) workerLoop(ctx context.Context, index int) {
	for {
		processed, err := s.ProcessOne(ctx, fmt.Sprintf("%s-%d", s.instanceID, index))
		if err != nil {
			slog.Warn("process taxonomy work", "worker", index, "err", err)
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
		// An invalid current snapshot cannot be silently repaired by mapping a
		// random challenge. Publisher is the only valid source of snapshots.
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
		ID:                newWorkID("mapping"),
		Kind:              WorkKindMapping,
		ChallengeID:       entry.ID,
		ChallengeRevision: entry.Revision,
		BaseRevision:      baseRevision,
		MapperSessionID:   claudecode.NewSessionID(),
		CurriculumSession: claudecode.NewSessionID(),
		SRESession:        claudecode.NewSessionID(),
		State:             WorkPending,
	})
	if err == nil {
		slog.Info("taxonomy mapping enqueued", "work", item.ID, "challenge", item.ChallengeID, "revision", item.ChallengeRevision)
	}
	return item, err
}

// ProcessOne advances one durable committee stage or publishes an approved
// candidate. Each call persists before releasing its worker lease.
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
	slog.Info("taxonomy work claimed", "work", item.ID, "challenge", item.ChallengeID, "revision", item.ChallengeRevision, "state", item.State, "round", item.Round)
	if item.Kind != WorkKindMapping {
		item.State = WorkFailed
		item.LastError = fmt.Sprintf("unsupported taxonomy work kind %q", item.Kind)
		return true, s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	}
	if item.State == WorkReadyPublish {
		return true, s.publish(ctx, item, workerID)
	}
	return true, s.runCommittee(ctx, item)
}

// runCommittee derives the next stage entirely from durable work state. A
// candidate with a complete review pair is a rejected semantic round and must
// return to the Mapper; a candidate without that pair belongs to both reviewers.
func (s *Service) runCommittee(ctx context.Context, item *WorkItem) error {
	entry, err := challenge.Get(s.challengesDir, item.ChallengeID)
	if err != nil || entry.Revision != item.ChallengeRevision {
		item.State = WorkCancelled
		item.LastError = "目标 challenge artifact 已不存在或 revision 已变化"
		return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	}
	if item.hasPartialReviews() {
		// Reviewer output is only meaningful as a complete pair. Clear legacy or
		// interrupted partial state before any retry path can persist it again.
		item.CurriculumReview = nil
		item.SREReview = nil
	}
	artifact, err := readArtifact(entry.Dir)
	if err != nil {
		return s.retryTechnical(ctx, item, fmt.Errorf("read challenge artifact: %w", err))
	}
	if item.needsMapper() {
		base, err := s.currentSnapshot()
		if err != nil {
			return s.retryTechnical(ctx, item, fmt.Errorf("load current taxonomy: %w", err))
		}
		return s.runMapperStage(ctx, item, *entry, base, artifact)
	}

	base, err := s.snapshotForRevision(item.BaseRevision)
	if err != nil {
		return s.retryTechnical(ctx, item, fmt.Errorf("load candidate taxonomy revision: %w", err))
	}
	return s.runReviewerStage(ctx, item, *entry, base, artifact)
}

func (item WorkItem) needsMapper() bool {
	return item.Candidate == nil || (item.CurriculumReview != nil && item.SREReview != nil)
}

func (item WorkItem) hasPartialReviews() bool {
	return item.Candidate != nil && (item.CurriculumReview == nil) != (item.SREReview == nil)
}

func (s *Service) runMapperStage(ctx context.Context, item *WorkItem, entry challenge.Entry, base Snapshot, artifact string) error {
	changes, err := s.runMapper(ctx, item, entry, base, artifact)
	if err != nil {
		return s.retryTechnical(ctx, item, err)
	}
	if _, err := validateMappingChangeSet(changes, entry, base); err != nil {
		return s.retryTechnical(ctx, item, fmt.Errorf("mapper candidate failed deterministic validation: %w", err))
	}
	item.BaseRevision = base.Revision
	item.Candidate = &changes
	item.CurriculumReview = nil
	item.SREReview = nil
	item.State = WorkPending
	item.LastError = ""
	item.TechnicalFailures = 0
	item.ExecutionFailures = 0
	item.NextRunAt = time.Time{}
	slog.Info("taxonomy mapper candidate persisted", "work", item.ID, "challenge", entry.ID, "round", item.Round+1)
	return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
}

func (s *Service) runReviewerStage(ctx context.Context, item *WorkItem, entry challenge.Entry, base Snapshot, artifact string) error {
	if item.Candidate == nil {
		return s.retryTechnical(ctx, item, errors.New("review stage has no mapper candidate"))
	}
	// A reviewer pair is indivisible. Legacy or interrupted partial results are
	// discarded before both reviewers are run again.
	item.CurriculumReview = nil
	item.SREReview = nil
	curriculum, sre, err := s.runReviews(ctx, item, entry, base, artifact, *item.Candidate)
	if err != nil {
		return s.retryTechnical(ctx, item, err)
	}
	item.Round++
	item.CurriculumReview = &curriculum
	item.SREReview = &sre
	item.TechnicalFailures = 0
	item.ExecutionFailures = 0
	item.NextRunAt = time.Time{}
	if curriculum.Decision == ReviewApprove && sre.Decision == ReviewApprove {
		item.State = WorkReadyPublish
	} else {
		// The same Mapper session receives both review results on its next fair
		// queue turn. There is intentionally no automatic retry limit.
		item.State = WorkPending
	}
	item.LastError = ""
	slog.Info("taxonomy review completed", "work", item.ID, "round", item.Round, "curriculum", curriculum.Decision, "sre", sre.Decision, "next_state", item.State)
	return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
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
		// The database may be unavailable, so this error cannot be durably
		// recorded through the same repository.
		return err
	}
	if !acquired {
		// Another Publisher will make progress; release this worker lease so the
		// Work List remains fair rather than waiting while holding it.
		return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
	}
	slog.Info("taxonomy publisher acquired lease", "work", item.ID, "challenge", item.ChallengeID)
	defer func() {
		if err := s.repo.ReleaseTaxonomyLease(context.Background(), publisherLeaseName, workerID); err != nil {
			slog.Warn("release taxonomy publisher lease", "err", err)
		}
	}()

	entry, err := challenge.Get(s.challengesDir, item.ChallengeID)
	if err != nil || entry.Revision != item.ChallengeRevision {
		item.State = WorkCancelled
		item.LastError = "目标 challenge artifact 已不存在或 revision 已变化"
		return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
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
	item.State = WorkPending
	item.LastError = reason
	item.TechnicalFailures = 0
	item.ExecutionFailures = 0
	item.NextRunAt = time.Time{}
	return s.repo.SaveClaimedTaxonomyWork(ctx, *item)
}

// retryTechnical preserves semantic progress. A failed execution re-enters the
// Work List with bounded exponential delay only after its round's shared agent
// failure budget is exhausted; active calls have already returned before this
// function is reached.
func (s *Service) retryTechnical(ctx context.Context, item *WorkItem, err error) error {
	item.TechnicalFailures++
	item.LastError = strings.TrimSpace(err.Error())
	if item.LastError == "" {
		item.LastError = "taxonomy work encountered an unspecified technical failure"
	}
	if item.State != WorkReadyPublish {
		item.State = WorkPending
	}
	item.NextRunAt = time.Time{}
	if item.TechnicalFailures >= maxTechnicalFailures {
		item.TechnicalFailures = 0
		item.ExecutionFailures++
		item.NextRunAt = time.Now().UTC().Add(retryDelay(item.ExecutionFailures))
		slog.Warn("taxonomy execution failure budget exhausted", "work", item.ID, "challenge", item.ChallengeID, "round", item.Round, "execution_failures", item.ExecutionFailures, "next_run_at", item.NextRunAt, "err", err)
	} else {
		slog.Warn("taxonomy technical failure", "work", item.ID, "challenge", item.ChallengeID, "round", item.Round, "technical_failures", item.TechnicalFailures, "err", err)
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
					slog.Warn("extend taxonomy work lease", "work", workID, "err", err)
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

func (s *Service) runMapper(ctx context.Context, item *WorkItem, entry challenge.Entry, base Snapshot, artifact string) (ChangeSet, error) {
	resume := item.MapperStarted
	if !item.MapperStarted {
		if err := s.repo.MarkTaxonomyWorkAgentStarted(ctx, item.ID, item.LeaseOwner, WorkAgentMapper); err != nil {
			return ChangeSet{}, err
		}
		item.MapperStarted = true
	}
	slog.Info("taxonomy mapper started", "work", item.ID, "challenge", entry.ID, "round", item.Round+1, "resume", resume)
	baseJSON, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return ChangeSet{}, err
	}
	var prior string
	if item.Candidate != nil {
		candidate, err := json.MarshalIndent(item.Candidate, "", "  ")
		if err != nil {
			return ChangeSet{}, err
		}
		prior = fmt.Sprintf("\n上一轮候选 ChangeSet：\n%s\n课程审核意见：%s\nSRE 审核意见：%s\n", candidate, formatReview(item.CurriculumReview), formatReview(item.SREReview))
	}
	if item.LastError != "" {
		prior += fmt.Sprintf("\n上一次处理信息：%s\n请基于当前 challenge、taxonomy 和候选上下文输出完整 ChangeSet。\n", item.LastError)
	}
	prompt := fmt.Sprintf(`已验证 challenge：
- id: %s
- title: %s
- revision: %s

当前 taxonomy snapshot：
%s

challenge 完整 artifact：
%s
%s
请输出一个完整 ChangeSet。它必须只映射这个 challenge；可以新增或修改 Skill、Tag 和 Skill.requires，但不得修改其他 challenge mapping。`, entry.ID, entry.Title, entry.Revision, baseJSON, artifact, prior)
	output, err := s.agent.Run(ctx, AgentRequest{Role: string(WorkAgentMapper), SessionID: item.MapperSessionID, Resume: resume, SystemPrompt: mapperSystemPrompt, Prompt: prompt, OutputSchema: changeSetOutputSchema})
	if err != nil {
		return ChangeSet{}, fmt.Errorf("run taxonomy mapper: %w", err)
	}
	var result ChangeSet
	if err := decodeStrictJSON(output, &result); err != nil {
		return ChangeSet{}, fmt.Errorf("mapper must return one valid ChangeSet JSON document: %w", err)
	}
	slog.Info("taxonomy mapper returned candidate", "work", item.ID, "challenge", entry.ID)
	for index := range result.ChallengeMappings {
		if result.ChallengeMappings[index].Value != nil && result.ChallengeMappings[index].Value.Challenge.ID == entry.ID {
			result.ChallengeMappings[index].Value.File = filepath.Base(entry.Dir)
		}
	}
	return result, nil
}

func (s *Service) runReviews(ctx context.Context, item *WorkItem, entry challenge.Entry, base Snapshot, artifact string, changes ChangeSet) (Review, Review, error) {
	slog.Info("taxonomy reviewers started", "work", item.ID, "challenge", entry.ID, "round", item.Round+1)
	baseJSON, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return Review{}, Review{}, err
	}
	changesJSON, err := json.MarshalIndent(changes, "", "  ")
	if err != nil {
		return Review{}, Review{}, err
	}
	prompt := fmt.Sprintf("已验证 challenge：%s (%s)\n\n当前 taxonomy：\n%s\n\n候选 ChangeSet：\n%s\n\nchallenge artifact：\n%s", entry.Title, entry.Revision, baseJSON, changesJSON, artifact)
	curriculumResume := item.CurriculumStarted
	sreResume := item.SREStarted
	if !item.CurriculumStarted {
		if err := s.repo.MarkTaxonomyWorkAgentStarted(ctx, item.ID, item.LeaseOwner, WorkAgentCurriculum); err != nil {
			return Review{}, Review{}, err
		}
		item.CurriculumStarted = true
	}
	if !item.SREStarted {
		if err := s.repo.MarkTaxonomyWorkAgentStarted(ctx, item.ID, item.LeaseOwner, WorkAgentSRE); err != nil {
			return Review{}, Review{}, err
		}
		item.SREStarted = true
	}
	type result struct {
		review Review
		err    error
	}
	var curriculum, sre result
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		output, err := s.agent.Run(ctx, AgentRequest{Role: string(WorkAgentCurriculum), SessionID: item.CurriculumSession, Resume: curriculumResume, SystemPrompt: curriculumReviewerPrompt, Prompt: prompt, OutputSchema: reviewOutputSchema})
		if err != nil {
			curriculum.err = fmt.Errorf("run curriculum reviewer: %w", err)
			return
		}
		curriculum.review, curriculum.err = decodeReview(output)
	}()
	go func() {
		defer wait.Done()
		output, err := s.agent.Run(ctx, AgentRequest{Role: string(WorkAgentSRE), SessionID: item.SRESession, Resume: sreResume, SystemPrompt: sreReviewerPrompt, Prompt: prompt, OutputSchema: reviewOutputSchema})
		if err != nil {
			sre.err = fmt.Errorf("run SRE reviewer: %w", err)
			return
		}
		sre.review, sre.err = decodeReview(output)
	}()
	wait.Wait()
	if curriculum.err != nil {
		return Review{}, Review{}, curriculum.err
	}
	if sre.err != nil {
		return Review{}, Review{}, sre.err
	}
	return curriculum.review, sre.review, nil
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

func decodeStrictJSON(input string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON documents")
		}
		return err
	}
	return nil
}

func decodeReview(input string) (Review, error) {
	var review Review
	if err := decodeStrictJSON(input, &review); err != nil {
		return Review{}, fmt.Errorf("reviewer must return one valid review JSON document: %w", err)
	}
	switch review.Decision {
	case ReviewApprove:
		if strings.TrimSpace(review.Feedback) != "" {
			return Review{}, errors.New("approve review must not include feedback")
		}
	case ReviewReject:
		if strings.TrimSpace(review.Feedback) == "" {
			return Review{}, errors.New("reject review must include concrete feedback")
		}
	default:
		return Review{}, fmt.Errorf("review decision must be approve or reject, got %q", review.Decision)
	}
	return review, nil
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

func newWorkID(prefix string) string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(bytes)
}

type claudeRunner struct {
	llm config.AgentConfig
}

func (r *claudeRunner) Run(ctx context.Context, request AgentRequest) (string, error) {
	options := []claudecode.Option{
		claudecode.WithSystemPrompt(request.SystemPrompt),
		claudecode.WithTools(),
		claudecode.WithPermissionMode("dontAsk"),
		claudecode.WithMaxTurns(8),
	}
	if strings.TrimSpace(request.OutputSchema) != "" {
		options = append(options,
			claudecode.WithStructuredOutput(request.OutputSchema),
			claudecode.WithEmitToolEvents(),
		)
	}
	if env := taxonomyClaudeEnvironment(r.llm); len(env) > 0 {
		options = append(options, claudecode.WithEnv(env...))
	}
	if request.Resume {
		options = append(options, claudecode.WithResume(request.SessionID))
	} else {
		options = append(options, claudecode.WithSessionID(request.SessionID))
	}
	agent, err := claudecode.New(options...)
	if err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctx, agentCallTimeout)
	defer cancel()
	events := agent.Run(runCtx, &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage(request.Prompt)},
	})
	var last, structured string
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			message := event.Output.MessageOutput.Message
			for _, call := range message.ToolCalls {
				if call.Function.Name == "StructuredOutput" {
					if structured != "" {
						return "", errors.New("agent emitted structured output more than once")
					}
					structured = strings.TrimSpace(call.Function.Arguments)
				}
			}
			if content := strings.TrimSpace(message.Content); content != "" {
				last = content
			}
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	if err := runCtx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(request.OutputSchema) != "" {
		if structured == "" {
			return "", errors.New("agent did not emit structured output")
		}
		return structured, nil
	}
	if strings.TrimSpace(last) == "" {
		return "", errors.New("agent returned an empty response")
	}
	return last, nil
}

func taxonomyClaudeEnvironment(llm config.AgentConfig) []string {
	values := []struct{ key, value string }{
		{"ANTHROPIC_BASE_URL", llm.BaseURL},
		{"ANTHROPIC_AUTH_TOKEN", llm.APIKey},
		{"ANTHROPIC_MODEL", llm.Model},
		{"ANTHROPIC_DEFAULT_OPUS_MODEL", llm.Model},
		{"ANTHROPIC_DEFAULT_SONNET_MODEL", llm.Model},
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.value) != "" {
			result = append(result, value.key+"="+value.value)
		}
	}
	return result
}

const mapperSystemPrompt = `你是 Breakfix Taxonomy Mapper。你只负责将已经真实验证并发布的 challenge 映射到独立的 Skill、Tag 和关系；不得修改 challenge 本身。

你必须只输出一个 JSON 对象，不能输出 Markdown、代码围栏、解释或任何额外文本。根对象只能有 skills、tags、challenge_mappings、skill_mappings 四个数组，且四个数组都必须出现。唯一允许的结构如下；字段名、嵌套层级和引用对象都必须完全一致：

{
  "skills": [
    {
      "operation": "upsert",
      "value": {
        "kind": "Skill",
        "id": "skill-7d6f1b7e522c4a21",
        "title": "可读标题",
        "definition": "能力定义",
        "mapping_guidance": {
          "outcome_when": ["何时作为练习结果"],
          "entry_when": ["何时只是进入题目的前置能力"],
          "exclude_when": ["何时不应归入此能力"]
        }
      }
    }
  ],
  "tags": [
    {
      "operation": "upsert",
      "value": {
        "kind": "Tag",
        "id": "tag-7d6f1b7e522c4a21",
        "title": "可读标题",
        "definition": "浏览维度定义",
        "mapping_guidance": {
          "include_when": ["何时应归入此浏览维度"],
          "exclude_when": ["何时不应归入此浏览维度"]
        }
      }
    }
  ],
  "challenge_mappings": [
    {
      "operation": "upsert",
      "value": {
        "challenge": {
          "id": "题目 id",
          "title": "题目 title",
          "revision": "题目 revision"
        },
        "tags": [{"id": "tag id", "title": "Tag title"}],
        "entry_skills": [{"id": "skill id", "title": "Skill title"}],
        "outcomes": [{"id": "skill id", "title": "Skill title", "primary": true}]
      }
    }
  ],
  "skill_mappings": [
    {
      "operation": "upsert",
      "value": {
        "source": {"id": "skill id", "title": "Skill title"},
        "requires": [{"id": "skill id", "title": "Skill title"}]
      }
    }
  ]
}

新增 ID 必须使用不包含题意的稳定 opaque ID：Skill 必须严格匹配 skill- 后接 16 位小写十六进制字符，Tag 必须严格匹配 tag- 后接 16 位小写十六进制字符。不得在 ChangeSet 根对象或任意 change wrapper 写 kind；kind 只能在 value 内的 Skill 或 Tag 上。不得使用 challenge_id、skill_id、checkpoint_id、裸字符串引用或 checkpoint 到 Skill 的映射。每个 challenge mapping 的 outcomes 全部合计必须恰好一个 primary=true。没有前置依赖的 Skill 不要创建 skill_mappings 条目。删除时只使用 {"operation":"delete","id":"..."}、{"operation":"delete","challenge_id":"..."} 或 {"operation":"delete","source_id":"..."}，且不得包含 value。

Skill 必须是可独立解释、能在多题复用的能力；Tag 仅用于受控的 Catalog 浏览维度，不能用 Tag 复述细粒度 Skill。优先复用现有定义，新增时才提出明确、可复用的定义。`

const changeSetOutputSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["skills", "tags", "challenge_mappings", "skill_mappings"],
  "properties": {
    "skills": {"type": "array"},
    "tags": {"type": "array"},
    "challenge_mappings": {"type": "array"},
    "skill_mappings": {"type": "array"}
  }
}`

const reviewOutputSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["decision"],
  "properties": {
    "decision": {"type": "string", "enum": ["approve", "reject"]},
    "feedback": {"type": "string"}
  }
}`

const curriculumReviewerPrompt = `你是 Breakfix Curriculum Reviewer。检查候选 taxonomy ChangeSet 是否建立了清楚、可复用且教学上合理的 Skill、Tag、entry/outcome 和 requires 关系。不要因 challenge 的基础镜像或偶然词汇误分类；不要把宽泛领域或单条命令参数伪装成 Skill。

只能输出一个 JSON 对象，禁止 Markdown 或额外文本。通过时必须恰好输出 {"decision":"approve"}；驳回时必须恰好输出 {"decision":"reject","feedback":"具体、可执行的中文问题"}。`

const sreReviewerPrompt = `你是 Breakfix SRE Reviewer。依据已验证 challenge artifact，检查候选 taxonomy ChangeSet 是否忠实描述实际排障目标、环境和检查点，而不是题意猜测；检查 Skill/Tag/entry/outcome/requires 是否有技术上的误导。

只能输出一个 JSON 对象，禁止 Markdown 或额外文本。通过时必须恰好输出 {"decision":"approve"}；驳回时必须恰好输出 {"decision":"reject","feedback":"具体、可执行的中文问题"}。`
