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

	"github.com/breakfix/breakfix/internal/agentruntime"
)

const (
	authoringPromptVersion = "authoring-v1"
	authoringRunDeadline   = agentruntime.ExecutionDeadline
)

// RuntimeRepository is the Server-owned Authoring boundary. Agent Workers only
// reach the mutable stage and finalization methods through internal HTTP APIs.
type RuntimeRepository interface {
	CreateAuthoringSession(context.Context, Session, Plan) (*Session, error)
	GetAuthoringSession(context.Context, string, string) (*Session, error)
	GetLatestOpenAuthoringSession(context.Context, string) (*Session, error)
	GetAuthoringRevision(context.Context, string, int64) (*Revision, error)
	StartAuthoringRun(context.Context, string, string, agentruntime.Message, agentruntime.CreateRun) (*Stage, *agentruntime.Run, error)
	ListMessages(context.Context, string) ([]agentruntime.Message, error)
}

// RuntimeService owns only user-facing Session and Run creation. It never
// invokes a model in the Server process.
type RuntimeService struct {
	repo  RuntimeRepository
	model string
}

func NewRuntimeService(repo RuntimeRepository, model string) *RuntimeService {
	return &RuntimeService{repo: repo, model: strings.TrimSpace(model)}
}

func (s *RuntimeService) Create(ctx context.Context, userID string) (*Session, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("authoring runtime repository is required")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("authoring session requires a user")
	}
	return s.repo.CreateAuthoringSession(ctx, Session{ID: NewID("author"), UserID: userID}, Plan{})
}

func (s *RuntimeService) Get(ctx context.Context, userID, sessionID string) (*Session, *Revision, []Message, error) {
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

func (s *RuntimeService) GetCurrent(ctx context.Context, userID string) (*Session, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("authoring runtime repository is required")
	}
	return s.repo.GetLatestOpenAuthoringSession(ctx, userID)
}

// StartTurn atomically creates the user message, Agent Run, and private stage.
// The response itself is finalized asynchronously by an Agent Worker.
func (s *RuntimeService) StartTurn(ctx context.Context, userID, sessionID, content string) (*Session, *agentruntime.Run, error) {
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
	if !stateAllowsAuthorMessage(session.State) || strings.TrimSpace(session.RuntimeSessionID) == "" {
		return nil, nil, ErrInvalidState
	}
	input, err := json.Marshal(struct {
		BaseRevision int64 `json:"base_revision"`
	}{BaseRevision: session.CurrentRevision})
	if err != nil {
		return nil, nil, fmt.Errorf("encode authoring run input: %w", err)
	}
	now := time.Now().UTC()
	_, run, err := s.repo.StartAuthoringRun(ctx, session.ID, userID, agentruntime.Message{
		ID:        agentruntime.NewID("authoring-message"),
		SessionID: session.RuntimeSessionID,
		Role:      "user",
		Content:   content,
		CreatedAt: now,
	}, agentruntime.CreateRun{
		ID:               agentruntime.NewID("authoring-run"),
		SessionID:        session.RuntimeSessionID,
		Purpose:          "authoring",
		OwnerKind:        "authoring-session",
		OwnerRef:         session.ID,
		InputRevision:    fmt.Sprintf("%d", session.CurrentRevision),
		Input:            input,
		Model:            s.model,
		PromptVersion:    authoringPromptVersion,
		ExecutionTimeout: authoringRunDeadline,
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

func projectRuntimeMessages(values []agentruntime.Message) ([]Message, error) {
	result := make([]Message, 0, len(values))
	for _, value := range values {
		role := value.Role
		switch role {
		case "user":
		case "assistant":
			role = "agent"
		default:
			return nil, fmt.Errorf("unsupported authoring runtime message role %q", value.Role)
		}
		message := Message{ID: value.ID, Role: role, Content: value.Content, CreatedAt: value.CreatedAt}
		if len(value.Metadata) > 0 && !bytes.Equal(value.Metadata, []byte("{}")) {
			var metadata struct {
				Changes []Change `json:"changes"`
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
