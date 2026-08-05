package roadmap

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	// MaxAgentCallsPerTask bounds semantic Planner/Reviewer calls for one task
	// role. Technical execution retries use agent.MaxAttempts inside one call.
	MaxAgentCallsPerTask      = 5
	AutomaticRequestThreshold = 20
)

var (
	ErrWorkflowNotFound = errors.New("roadmap workflow not found")
	ErrTaskNotFound     = errors.New("roadmap task not found")
	ErrLeaseLost        = errors.New("roadmap maintenance lease was lost")
	ErrAgentCallLimit   = errors.New("roadmap task agent call limit reached")
)

// WorkflowState models only the durable lifecycle of one fixed Roadmap entry
// snapshot. Agent task failure is an expected task result, not a workflow
// failure: the source entry remains pending for a later incremental run.
type WorkflowState string

const (
	WorkflowQueued     WorkflowState = "Queued"
	WorkflowRunning    WorkflowState = "Running"
	WorkflowPublishing WorkflowState = "Publishing"
	WorkflowCompleted  WorkflowState = "Completed"
)

func (s WorkflowState) Valid() bool {
	switch s {
	case WorkflowQueued, WorkflowRunning, WorkflowPublishing, WorkflowCompleted:
		return true
	default:
		return false
	}
}

func (s WorkflowState) Terminal() bool { return s == WorkflowCompleted }

// TaskKind selects the independent graph that the task may change.
type TaskKind string

const (
	TaskTopic     TaskKind = "Topic"
	TaskChallenge TaskKind = "Challenge"
)

func (k TaskKind) Valid() bool { return k == TaskTopic || k == TaskChallenge }

// TaskState intentionally has no retry state. A failed task remains pending
// through its source entry and is rebuilt only by a later Roadmap workflow.
type TaskState string

const (
	TaskPending  TaskState = "Pending"
	TaskRunning  TaskState = "Running"
	TaskAccepted TaskState = "Accepted"
	TaskFailed   TaskState = "Failed"
)

func (s TaskState) Valid() bool {
	switch s {
	case TaskPending, TaskRunning, TaskAccepted, TaskFailed:
		return true
	default:
		return false
	}
}

func (s TaskState) Terminal() bool { return s == TaskAccepted || s == TaskFailed }

type AgentRole string

const (
	AgentPlanner            AgentRole = "planner"
	AgentCurriculumReviewer AgentRole = "curriculum-reviewer"
	AgentSREReviewer        AgentRole = "sre-reviewer"
)

func (r AgentRole) Valid() bool {
	return r == AgentPlanner || r == AgentCurriculumReviewer || r == AgentSREReviewer
}

func (r AgentRole) Purpose() string {
	switch r {
	case AgentPlanner:
		return "roadmap-planner"
	case AgentCurriculumReviewer:
		return "roadmap-curriculum-reviewer"
	case AgentSREReviewer:
		return "roadmap-sre-reviewer"
	default:
		return ""
	}
}

func (r AgentRole) PromptVersion() string {
	switch r {
	case AgentPlanner:
		return "roadmap-planner-v1"
	case AgentCurriculumReviewer:
		return "roadmap-curriculum-reviewer-v1"
	case AgentSREReviewer:
		return "roadmap-sre-reviewer-v1"
	default:
		return ""
	}
}

type ReviewDecision string

const (
	ReviewApproved ReviewDecision = "approved"
	ReviewRejected ReviewDecision = "rejected"
)

type Review struct {
	Decision ReviewDecision `json:"decision"`
	Feedback string         `json:"feedback,omitempty"`
}

func (r Review) Validate() error {
	switch r.Decision {
	case ReviewApproved:
		if strings.TrimSpace(r.Feedback) != "" {
			return errors.New("approved roadmap review must not include feedback")
		}
	case ReviewRejected:
		if strings.TrimSpace(r.Feedback) == "" {
			return errors.New("rejected roadmap review requires feedback")
		}
	default:
		return fmt.Errorf("invalid roadmap review decision %q", r.Decision)
	}
	return nil
}

// Subject carries the frozen entity identity used by one task. Challenge
// subjects also retain their immutable content revision so a vanished or
// replaced artifact cannot be mistaken for the task's original input.
type Subject struct {
	Ref             Ref    `json:"ref"`
	ContentRevision string `json:"content_revision,omitempty"`
}

func (s Subject) Valid(kind TaskKind) bool {
	if strings.TrimSpace(s.Ref.ID) == "" || strings.TrimSpace(s.Ref.SourceRef) == "" || strings.TrimSpace(s.Ref.Title) == "" {
		return false
	}
	return kind != TaskChallenge || ValidRevision(s.ContentRevision)
}

// Entry is the durable incremental-maintenance marker created alongside one
// published Challenge binding. It deliberately contains no mutable graph
// result: processing facts are advanced only after a workflow publishes.
type Entry struct {
	ChallengeID        string    `json:"challenge_id"`
	TopicID            string    `json:"topic_id"`
	TopicProcessed     bool      `json:"topic_processed"`
	ChallengeProcessed bool      `json:"challenge_processed"`
	CreatedAt          time.Time `json:"created_at"`
}

func (e Entry) Pending() bool { return !e.TopicProcessed || !e.ChallengeProcessed }

type WorkflowEntry struct {
	WorkflowID        string `json:"workflow_id"`
	ChallengeID       string `json:"challenge_id"`
	TopicID           string `json:"topic_id"`
	SnapshotOrder     int    `json:"snapshot_order"`
	TopicRequired     bool   `json:"topic_required"`
	ChallengeRequired bool   `json:"challenge_required"`
}

type Workflow struct {
	ID              string        `json:"id"`
	BaseRevision    string        `json:"base_revision"`
	State           WorkflowState `json:"state"`
	PublishAttempts int           `json:"publish_attempts"`
	LeaseOwner      string        `json:"-"`
	LeaseVersion    int           `json:"-"`
	LeaseExpiresAt  *time.Time    `json:"lease_expires_at,omitempty"`
	NextRunAt       time.Time     `json:"next_run_at"`
	LastError       string        `json:"last_error,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

func (w Workflow) Valid() bool {
	return strings.TrimSpace(w.ID) != "" && ValidRevision(w.BaseRevision) && w.State.Valid() &&
		w.PublishAttempts >= 0 && w.LeaseVersion >= 0 && !w.NextRunAt.IsZero() && !w.CreatedAt.IsZero() && !w.UpdatedAt.IsZero()
}

type Task struct {
	ID               string     `json:"id"`
	WorkflowID       string     `json:"workflow_id"`
	EntryChallengeID string     `json:"entry_challenge_id"`
	SnapshotOrder    int        `json:"snapshot_order"`
	Kind             TaskKind   `json:"kind"`
	Subject          Subject    `json:"subject"`
	State            TaskState  `json:"state"`
	Round            int        `json:"round"`
	PlannerCalls     int        `json:"planner_calls"`
	CurriculumCalls  int        `json:"curriculum_calls"`
	SRECalls         int        `json:"sre_calls"`
	ChangeSet        *ChangeSet `json:"change_set,omitempty"`
	CurriculumReview *Review    `json:"curriculum_review,omitempty"`
	SREReview        *Review    `json:"sre_review,omitempty"`
	LeaseOwner       string     `json:"-"`
	LeaseVersion     int        `json:"-"`
	LeaseExpiresAt   *time.Time `json:"lease_expires_at,omitempty"`
	NextRunAt        time.Time  `json:"next_run_at"`
	LastError        string     `json:"last_error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func (t Task) Valid() bool {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.WorkflowID) == "" || strings.TrimSpace(t.EntryChallengeID) == "" ||
		t.SnapshotOrder < 0 || !t.Kind.Valid() || !t.Subject.Valid(t.Kind) || !t.State.Valid() || t.Round < 0 ||
		t.PlannerCalls < 0 || t.CurriculumCalls < 0 || t.SRECalls < 0 || t.LeaseVersion < 0 || t.NextRunAt.IsZero() ||
		t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return false
	}
	return t.PlannerCalls <= MaxAgentCallsPerTask && t.CurriculumCalls <= MaxAgentCallsPerTask && t.SRECalls <= MaxAgentCallsPerTask
}

type LeaseCredential struct {
	LeaseOwner   string `json:"lease_owner"`
	LeaseVersion int    `json:"lease_version"`
}

func (c LeaseCredential) Valid() bool {
	return strings.TrimSpace(c.LeaseOwner) != "" && c.LeaseVersion > 0
}

type WorkflowClaim struct {
	Workflow Workflow `json:"workflow"`
	LeaseCredential
}

func (c WorkflowClaim) Valid() bool { return c.Workflow.Valid() && c.LeaseCredential.Valid() }

type TaskClaim struct {
	Task Task `json:"task"`
	LeaseCredential
}

func (c TaskClaim) Valid() bool { return c.Task.Valid() && c.LeaseCredential.Valid() }

// ChangeSet is intentionally small: Roadmap maintenance only proposes graph
// edges for its one subject. Definitions and classification are immutable
// inputs owned by their separate authoring stage.
type ChangeSet struct {
	Edges []Edge `json:"edges"`
}

func (c ChangeSet) Clone() ChangeSet {
	return ChangeSet{Edges: append([]Edge(nil), c.Edges...)}
}

// ValidateFor makes a planner candidate deterministic without interpreting
// its educational meaning. An empty edge list is a valid conclusion.
func (c ChangeSet) ValidateFor(kind TaskKind, subject Subject, revision Revision) error {
	if !kind.Valid() || !subject.Valid(kind) || !ValidRevision(revision.Revision) {
		return errors.New("roadmap changeset requires a valid task subject and immutable revision")
	}
	var values map[string]Ref
	challengeRevisions := map[string]string(nil)
	switch kind {
	case TaskTopic:
		values = make(map[string]Ref, len(revision.Topics))
		for _, value := range revision.Topics {
			values[value.ID] = Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
		}
	case TaskChallenge:
		values = make(map[string]Ref, len(revision.ChallengeBindings))
		challengeRevisions = make(map[string]string, len(revision.ChallengeBindings))
		for _, value := range revision.ChallengeBindings {
			values[value.Challenge.ID] = Ref{ID: value.Challenge.ID, SourceRef: value.Challenge.SourceRef, Title: value.Challenge.Title}
			challengeRevisions[value.Challenge.ID] = value.Challenge.ContentRevision
		}
	}
	if actual, exists := values[subject.Ref.ID]; !exists || actual != subject.Ref {
		return errors.New("roadmap task subject is absent from its fixed revision")
	}
	if kind == TaskChallenge && challengeRevisions[subject.Ref.ID] != subject.ContentRevision {
		return errors.New("roadmap task challenge content revision does not match its fixed revision")
	}
	seen := make(map[string]struct{}, len(c.Edges))
	for _, edge := range c.Edges {
		if !edge.Relation.valid() || strings.TrimSpace(edge.Reason) == "" || edge.Source.ID == edge.Target.ID {
			return errors.New("roadmap changeset contains an invalid edge")
		}
		source, sourceExists := values[edge.Source.ID]
		target, targetExists := values[edge.Target.ID]
		if !sourceExists || !targetExists || edge.Source != source || edge.Target != target {
			return errors.New("roadmap changeset references an entity outside its fixed revision")
		}
		if edge.Source.ID != subject.Ref.ID && edge.Target.ID != subject.Ref.ID {
			return errors.New("roadmap changeset edge does not include the task subject")
		}
		key := edge.Source.ID + "\x00" + edge.Target.ID + "\x00" + string(edge.Relation)
		if edge.Relation == RelationRelated && edge.Source.ID > edge.Target.ID {
			key = edge.Target.ID + "\x00" + edge.Source.ID + "\x00" + string(edge.Relation)
		}
		if _, exists := seen[key]; exists {
			return errors.New("roadmap changeset duplicates an edge")
		}
		seen[key] = struct{}{}
	}
	return nil
}

type TaskChangeSet struct {
	TaskID        string    `json:"task_id"`
	SnapshotOrder int       `json:"snapshot_order"`
	ChangeSet     ChangeSet `json:"change_set"`
}

type MergeAudit struct {
	TaskID  string `json:"task_id,omitempty"`
	Edge    Edge   `json:"edge"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// MergeEdges applies accepted task candidates in stable snapshot order. It
// never changes the model's semantic judgement, only resolves deterministic
// graph constraints before a complete revision is published.
func MergeEdges(existing []Edge, changes []TaskChangeSet) ([]Edge, []MergeAudit, error) {
	for _, edge := range existing {
		if !edge.Relation.valid() || strings.TrimSpace(edge.Reason) == "" || edge.Source.ID == edge.Target.ID {
			return nil, nil, errors.New("existing roadmap graph contains an invalid edge")
		}
	}
	ordered := flattenChanges(changes)
	pairs := make(map[string]*edgePair, len(existing)+len(ordered))
	for _, edge := range existing {
		pair := ensureEdgePair(pairs, edgePairKey(edge))
		pair.add(edgeRecord{edge: canonicalRelated(edge), existing: true})
	}
	for _, value := range ordered {
		pair := ensureEdgePair(pairs, edgePairKey(value.edge))
		pair.add(value)
	}

	keys := make([]string, 0, len(pairs))
	for key := range pairs {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	acceptedExisting := make([]Edge, 0, len(existing))
	acceptedCandidates := make([]edgeRecord, 0, len(ordered))
	audits := make([]MergeAudit, 0, len(ordered))
	for _, key := range keys {
		pair := pairs[key]
		resolved, pairAudits := pair.resolve()
		audits = append(audits, pairAudits...)
		if resolved.existing {
			acceptedExisting = append(acceptedExisting, resolved.edge)
		} else {
			acceptedCandidates = append(acceptedCandidates, resolved)
		}
	}

	slices.SortFunc(acceptedExisting, compareEdges)
	slices.SortFunc(acceptedCandidates, compareEdgeRecords)
	graph := make(map[string][]string, len(acceptedExisting)+len(acceptedCandidates))
	result := append([]Edge(nil), acceptedExisting...)
	for _, edge := range acceptedExisting {
		if edge.Relation == RelationPrecedes {
			graph[edge.Source.ID] = append(graph[edge.Source.ID], edge.Target.ID)
		}
	}
	for _, record := range acceptedCandidates {
		edge := record.edge
		if edge.Relation == RelationPrecedes && reachable(graph, edge.Target.ID, edge.Source.ID) {
			audits = append(audits, MergeAudit{TaskID: record.taskID, Edge: edge, Outcome: "ignored_cycle", Reason: "插入该 precedes 边会形成有向环"})
			continue
		}
		result = append(result, edge)
		if edge.Relation == RelationPrecedes {
			graph[edge.Source.ID] = append(graph[edge.Source.ID], edge.Target.ID)
		}
	}
	slices.SortFunc(result, compareEdges)
	slices.SortFunc(audits, func(left, right MergeAudit) int {
		if value := compare(left.TaskID, right.TaskID); value != 0 {
			return value
		}
		if value := compareEdges(left.Edge, right.Edge); value != 0 {
			return value
		}
		return compare(left.Outcome, right.Outcome)
	})
	return result, audits, nil
}

type edgeRecord struct {
	edge          Edge
	taskID        string
	snapshotOrder int
	edgeOrder     int
	existing      bool
}

func flattenChanges(changes []TaskChangeSet) []edgeRecord {
	result := make([]edgeRecord, 0)
	for _, change := range changes {
		for index, edge := range change.ChangeSet.Edges {
			result = append(result, edgeRecord{edge: canonicalRelated(edge), taskID: change.TaskID, snapshotOrder: change.SnapshotOrder, edgeOrder: index})
		}
	}
	slices.SortFunc(result, compareEdgeRecords)
	return result
}

func compareEdgeRecords(left, right edgeRecord) int {
	if left.existing != right.existing {
		if left.existing {
			return -1
		}
		return 1
	}
	if left.snapshotOrder != right.snapshotOrder {
		if left.snapshotOrder < right.snapshotOrder {
			return -1
		}
		return 1
	}
	if value := compare(left.taskID, right.taskID); value != 0 {
		return value
	}
	if value := compareEdges(left.edge, right.edge); value != 0 {
		return value
	}
	if left.edgeOrder < right.edgeOrder {
		return -1
	}
	if left.edgeOrder > right.edgeOrder {
		return 1
	}
	return 0
}

type edgePair struct {
	related []edgeRecord
	forward []edgeRecord
	reverse []edgeRecord
}

func ensureEdgePair(values map[string]*edgePair, key string) *edgePair {
	if value := values[key]; value != nil {
		return value
	}
	value := &edgePair{}
	values[key] = value
	return value
}

func (p *edgePair) add(record edgeRecord) {
	if record.edge.Relation == RelationRelated {
		p.related = append(p.related, record)
		return
	}
	if record.edge.Source.SourceRef < record.edge.Target.SourceRef {
		p.forward = append(p.forward, record)
		return
	}
	p.reverse = append(p.reverse, record)
}

func (p *edgePair) resolve() (edgeRecord, []MergeAudit) {
	slices.SortFunc(p.related, compareEdgeRecords)
	slices.SortFunc(p.forward, compareEdgeRecords)
	slices.SortFunc(p.reverse, compareEdgeRecords)
	audits := make([]MergeAudit, 0)
	if len(p.related) > 0 {
		selected := p.related[0]
		for _, candidate := range append(append([]edgeRecord(nil), p.related[1:]...), append(p.forward, p.reverse...)...) {
			if candidate.existing {
				if !selected.existing && candidate.edge.Relation == RelationPrecedes {
					audits = append(audits, MergeAudit{Edge: candidate.edge, Outcome: "related_precedence", Reason: "新的 related 关系优先于同一实体对的既有 precedes"})
				}
				continue
			}
			outcome := "ignored_duplicate"
			reason := "同一实体对已存在 related 边"
			if candidate.edge.Relation == RelationPrecedes {
				outcome = "related_precedence"
				reason = "related 关系优先于同一实体对的 precedes"
			}
			audits = append(audits, MergeAudit{TaskID: candidate.taskID, Edge: candidate.edge, Outcome: outcome, Reason: reason})
		}
		if !selected.existing {
			audits = append(audits, MergeAudit{TaskID: selected.taskID, Edge: selected.edge, Outcome: "accepted_related", Reason: "related 关系按无向规范化写入"})
		}
		selected.edge = canonicalRelated(selected.edge)
		return selected, audits
	}
	if len(p.forward) > 0 && len(p.reverse) > 0 {
		selected := p.forward[0]
		if compareEdgeRecords(p.reverse[0], selected) < 0 {
			selected = p.reverse[0]
		}
		converted := Edge{Source: selected.edge.Source, Target: selected.edge.Target, Relation: RelationRelated, Reason: selected.edge.Reason}
		converted = canonicalRelated(converted)
		selected.edge = converted
		for _, candidate := range append(append([]edgeRecord(nil), p.forward...), p.reverse...) {
			if candidate.existing {
				continue
			}
			audits = append(audits, MergeAudit{TaskID: candidate.taskID, Edge: candidate.edge, Outcome: "converted_related", Reason: "双向 precedes 合并为 related"})
		}
		return selected, audits
	}
	values := p.forward
	if len(values) == 0 {
		values = p.reverse
	}
	selected := values[0]
	for _, candidate := range values[1:] {
		if candidate.existing {
			continue
		}
		audits = append(audits, MergeAudit{TaskID: candidate.taskID, Edge: candidate.edge, Outcome: "ignored_duplicate", Reason: "同向 precedes 边已存在"})
	}
	if !selected.existing {
		audits = append(audits, MergeAudit{TaskID: selected.taskID, Edge: selected.edge, Outcome: "accepted_precedes", Reason: "通过确定性关系约束，等待无环检查"})
	}
	return selected, audits
}

func edgePairKey(edge Edge) string {
	left, right := edge.Source.SourceRef, edge.Target.SourceRef
	if left > right {
		left, right = right, left
	}
	return left + "\x00" + right
}

func canonicalRelated(edge Edge) Edge {
	if edge.Relation == RelationRelated && edge.Source.SourceRef > edge.Target.SourceRef {
		edge.Source, edge.Target = edge.Target, edge.Source
	}
	return edge
}

func reachable(graph map[string][]string, start, target string) bool {
	if start == target {
		return true
	}
	seen := map[string]struct{}{start: {}}
	queue := []string{start}
	for len(queue) > 0 {
		value := queue[0]
		queue = queue[1:]
		children := append([]string(nil), graph[value]...)
		slices.Sort(children)
		for _, child := range children {
			if child == target {
				return true
			}
			if _, exists := seen[child]; exists {
				continue
			}
			seen[child] = struct{}{}
			queue = append(queue, child)
		}
	}
	return false
}

func NewWorkflowID() string { return newMaintenanceID("roadmap-workflow") }
func NewTaskID() string     { return newMaintenanceID("roadmap-task") }
func NewLeaseOwner(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "roadmap-server"
	}
	return prefix + "-" + newMaintenanceID("lease")
}

func newMaintenanceID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}

func RetryAt(attempt int, now time.Time) time.Time {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 6))
	if delay > time.Minute {
		delay = time.Minute
	}
	return now.UTC().Add(delay)
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
