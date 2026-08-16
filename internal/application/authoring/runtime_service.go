package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	domain "github.com/breakfix/breakfix/internal/domain/authoring"
)

const (
	authoringPromptVersion = "authoring-v2"
	defaultRunDeadline     = 30 * time.Minute
)

// RuntimeRepository is the Server-owned Authoring boundary. The Server owns
// the mutable stage and finalization methods for direct authoring turns.
type RuntimeRepository interface {
	CreateAuthoringSession(context.Context, domain.Session, domain.Plan) (*domain.Session, error)
	CreateChallengeRevisionSession(context.Context, string, string) (*domain.Session, error)
	GetAuthoringSession(context.Context, string, string) (*domain.Session, error)
	GetAuthoringSessionInternal(context.Context, string) (*domain.Session, error)
	GetLatestOpenAuthoringSession(context.Context, string) (*domain.Session, error)
	GetAuthoringRevision(context.Context, string, int64) (*domain.Revision, error)
	StartAuthoringRun(context.Context, string, string, agent.Message, agent.CreateRun) (*domain.Stage, *agent.Run, error)
	ListMessages(context.Context, string) ([]agent.Message, error)
	GetRun(context.Context, string) (*agent.Run, error)
	LoadAuthoringExecution(context.Context, string) (*domain.Stage, []agent.Message, error)
	UpdateAuthoringStage(context.Context, string, int, int64, domain.StageOperation, domain.Plan, domain.Change) (*domain.Stage, error)
	FinalizeAuthoringRun(context.Context, string, int, string, time.Time) (*domain.Revision, error)
	RestartInterruptedAuthoringRun(context.Context, string, string, time.Time) (*agent.Run, error)
}

// Execution is the trusted Server-owned context for one Authoring AgentRun.
// User and session identity are read from durable state rather than supplied by
// a model tool call.
type Execution struct {
	RunID         string
	UserID        string
	SessionID     string
	UserMessageID string
	Stage         domain.Stage
	History       []agent.Message
}

// Executor owns the model call for one interactive authoring run. The Eino
// implementation lives in adapter/llm; this package owns only state changes.
type Executor interface {
	Run(context.Context, Execution, StageUpdater, func(StreamEvent)) (string, error)
}

type StageUpdater interface {
	UpdateAuthoringStage(context.Context, string, int, int64, domain.StageOperation, domain.Plan, domain.Change) (*domain.Stage, error)
}

type StreamEvent struct {
	Content string
}

// RuntimeService owns user-facing authoring state and direct interactive turn
// completion. It delegates model execution through Executor.
type RuntimeService struct {
	repo     RuntimeRepository
	model    string
	deadline time.Duration
	executor Executor
}

func NewRuntimeService(repo RuntimeRepository, model string, deadline time.Duration, executor Executor) *RuntimeService {
	if deadline <= 0 {
		deadline = defaultRunDeadline
	}
	return &RuntimeService{repo: repo, model: strings.TrimSpace(model), deadline: deadline, executor: executor}
}

func (s *RuntimeService) Create(ctx context.Context, userID string) (*domain.Session, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("authoring runtime repository is required")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("authoring session requires a user")
	}
	return s.repo.CreateAuthoringSession(ctx, domain.Session{ID: domain.NewID("author"), UserID: userID}, domain.Plan{})
}

// CreateRevision starts a new authoring conversation from the current active
// revision of an author-owned Challenge. The repository records the base
// revision fence; publishing still uses the complete generation pipeline.
func (s *RuntimeService) CreateRevision(ctx context.Context, userID, challengeID string) (*domain.Session, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("authoring runtime repository is required")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(challengeID) == "" {
		return nil, errors.New("challenge revision session requires user and challenge")
	}
	return s.repo.CreateChallengeRevisionSession(ctx, userID, challengeID)
}

func (s *RuntimeService) Get(ctx context.Context, userID, sessionID string) (*domain.Session, *domain.Revision, []domain.Message, error) {
	if s == nil || s.repo == nil {
		return nil, nil, nil, errors.New("authoring runtime repository is required")
	}
	session, err := s.repo.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	visibleRevision := session.CurrentRevision
	if session.VisibleRevision > 0 {
		visibleRevision = session.VisibleRevision
	}
	revision, err := s.repo.GetAuthoringRevision(ctx, session.ID, visibleRevision)
	if err != nil {
		return nil, nil, nil, err
	}
	if strings.TrimSpace(session.RuntimeSessionID) == "" {
		return nil, nil, nil, errors.New("authoring session has no runtime session")
	}
	runtimeMessages, err := s.repo.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil {
		return nil, nil, nil, err
	}
	messages, err := projectRuntimeMessages(runtimeMessages)
	if err != nil {
		return nil, nil, nil, err
	}
	return session, revision, messages, nil
}

func (s *RuntimeService) GetCurrent(ctx context.Context, userID string) (*domain.Session, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("authoring runtime repository is required")
	}
	return s.repo.GetLatestOpenAuthoringSession(ctx, userID)
}

// StartTurn atomically creates the user message, Agent Run, and private stage.
// The Server then executes and streams this turn in the request that created it.
func (s *RuntimeService) StartTurn(ctx context.Context, userID, sessionID, content string) (*domain.Session, *agent.Run, error) {
	if s == nil || s.repo == nil {
		return nil, nil, errors.New("authoring runtime repository is required")
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil, errors.New("消息不能为空")
	}
	if s.model == "" {
		return nil, nil, errors.New("authoring agent model is required")
	}
	session, err := s.repo.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		return nil, nil, err
	}
	if !domain.AllowsAuthorMessage(session.State) || strings.TrimSpace(session.RuntimeSessionID) == "" {
		return nil, nil, domain.ErrInvalidState
	}
	input, err := json.Marshal(struct {
		BaseRevision int64 `json:"base_revision"`
	}{BaseRevision: session.CurrentRevision})
	if err != nil {
		return nil, nil, fmt.Errorf("encode authoring run input: %w", err)
	}
	now := time.Now().UTC()
	_, run, err := s.repo.StartAuthoringRun(ctx, session.ID, userID, agent.Message{
		ID:        agent.NewID("authoring-message"),
		SessionID: session.RuntimeSessionID,
		Role:      "user",
		Content:   content,
		CreatedAt: now,
	}, agent.CreateRun{
		ID:            agent.NewID("authoring-run"),
		SessionID:     session.RuntimeSessionID,
		Purpose:       "authoring",
		OwnerKind:     "authoring-session",
		OwnerRef:      session.ID,
		InputRevision: fmt.Sprintf("%d", session.CurrentRevision),
		Input:         input,
		Model:         s.model,
		PromptVersion: authoringPromptVersion,
		DeadlineAt:    now.Add(s.deadline),
	})
	if err != nil {
		return nil, nil, err
	}
	updated, err := s.repo.GetAuthoringSession(ctx, session.ID, userID)
	if err != nil {
		return nil, nil, err
	}
	return updated, run, nil
}

func (s *RuntimeService) RunTurn(ctx context.Context, runID string, emit func(StreamEvent)) (string, error) {
	if s == nil || s.repo == nil || s.executor == nil {
		return "", errors.New("authoring turn executor is required")
	}
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return "", err
	}
	if run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || run.Status != agent.RunRunning {
		return "", agent.ErrRunActive
	}
	stage, history, err := s.repo.LoadAuthoringExecution(ctx, run.ID)
	if err != nil {
		return "", err
	}
	if stage.RunAttempt != run.Attempt {
		return "", agent.ErrRunActive
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		return "", errors.New("authoring execution has no latest user message")
	}
	session, err := s.repo.GetAuthoringSessionInternal(ctx, stage.SessionID)
	if err != nil {
		return "", err
	}
	if session.ID != run.OwnerRef || session.RuntimeSessionID != run.SessionID || strings.TrimSpace(session.UserID) == "" {
		return "", agent.ErrRunActive
	}
	execution := Execution{
		RunID: run.ID, UserID: session.UserID, SessionID: session.ID, UserMessageID: history[len(history)-1].ID,
		Stage: *stage, History: history,
	}
	runCtx, cancel := context.WithDeadline(ctx, run.DeadlineAt)
	defer cancel()
	content, err := s.executor.Run(runCtx, execution, s.repo, emit)
	if err != nil {
		return "", err
	}
	if _, err := s.repo.FinalizeAuthoringRun(runCtx, run.ID, run.Attempt, content, time.Now().UTC()); err != nil {
		return "", err
	}
	return content, nil
}

// RestartInterruptedTurn replaces a Server-interrupted interactive execution
// with a fresh Run. Its private stage is reconstructed from the committed Plan
// and durable user message rather than resumed from prior model memory.
func (s *RuntimeService) RestartInterruptedTurn(ctx context.Context, runID, reason string) (*agent.Run, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("authoring runtime repository is required")
	}
	return s.repo.RestartInterruptedAuthoringRun(ctx, runID, reason, time.Now().UTC())
}

func projectRuntimeMessages(values []agent.Message) ([]domain.Message, error) {
	result := make([]domain.Message, 0, len(values))
	for _, value := range values {
		role := value.Role
		switch role {
		case "user":
		case "assistant":
			role = "agent"
		default:
			return nil, fmt.Errorf("unsupported authoring runtime message role %q", value.Role)
		}
		message := domain.Message{ID: value.ID, Role: role, Content: value.Content, CreatedAt: value.CreatedAt}
		if len(value.Metadata) > 0 && !bytes.Equal(value.Metadata, []byte("{}")) {
			var metadata struct {
				Changes []domain.Change `json:"changes"`
			}
			decoder := json.NewDecoder(bytes.NewReader(value.Metadata))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&metadata); err != nil {
				return nil, fmt.Errorf("decode authoring message metadata: %w", err)
			}
			var extra any
			if err := decoder.Decode(&extra); err == nil {
				return nil, errors.New("authoring message metadata has a second JSON document")
			} else if !errors.Is(err, io.EOF) {
				return nil, fmt.Errorf("decode authoring message metadata suffix: %w", err)
			}
			message.Changes = metadata.Changes
		}
		result = append(result, message)
	}
	return result, nil
}
