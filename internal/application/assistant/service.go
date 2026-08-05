package assistant

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
)

const (
	promptVersion = "assistant-v1"
)

type Service struct {
	repo     agent.Repository
	model    string
	executor Executor
}

func NewService(repo agent.Repository, model string, executor Executor) *Service {
	return &Service{repo: repo, model: strings.TrimSpace(model), executor: executor}
}

func (s *Service) GetOrCreate(ctx context.Context, request Request) (*Session, []Message, error) {
	if err := ValidateRequest(request); err != nil {
		return nil, nil, err
	}
	if s.repo == nil {
		return nil, nil, errors.New("assistant runtime repository is required")
	}
	session, err := s.repo.FindOrCreateSession(ctx, agent.Session{
		ID:        NewID("assistant"),
		Purpose:   "assistant",
		OwnerKind: "environment",
		OwnerRef:  request.EnvironmentUID,
		UserRef:   request.UserID,
	})
	if err != nil {
		return nil, nil, err
	}
	messages, err := s.repo.ListMessages(ctx, session.ID)
	if err != nil {
		return nil, nil, err
	}
	projected, err := projectMessages(messages)
	if err != nil {
		return nil, nil, err
	}
	return sessionProjection(session, request), projected, nil
}

// StartTurn stores the user message and an already-running Server-owned call
// in one transaction. The caller directly executes the model after this
// method returns.
func (s *Service) StartTurn(ctx context.Context, request Request, content string) (*Session, *agent.Run, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil, errors.New("消息不能为空")
	}
	session, _, err := s.GetOrCreate(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	input, err := json.Marshal(RunInput{CurrentNode: request.CurrentNode, CurrentWindow: request.CurrentWindow, Terminals: cloneTerminalContexts(request.Terminals)})
	if err != nil {
		return nil, nil, fmt.Errorf("encode assistant run input: %w", err)
	}
	run, err := s.repo.CreateMessageAndRun(ctx, agent.Message{
		ID:        NewID("assistant-message"),
		SessionID: session.ID,
		Role:      "user",
		Content:   content,
		CreatedAt: now,
	}, agent.CreateRun{
		ID:            NewID("assistant-run"),
		SessionID:     session.ID,
		Purpose:       "assistant",
		OwnerKind:     "environment",
		OwnerRef:      request.EnvironmentUID,
		InputRevision: request.EnvironmentUID,
		Input:         input,
		Model:         s.model,
		PromptVersion: promptVersion,
	})
	if errors.Is(err, agent.ErrRunActive) {
		return nil, nil, ErrTurnRunning
	}
	if err != nil {
		return nil, nil, err
	}
	return session, run, nil
}

// RunTurn loads the durable conversation, delegates model execution to its
// adapter, then atomically persists the final assistant message. A known
// technical error starts a fresh Eino instance for the same AgentRun, bounded
// by the Run's five-attempt budget. Server shutdown leaves the Run running so
// startup recovery can replace it from durable conversation facts.
func (s *Service) RunTurn(ctx context.Context, sessionID, runID string, request Request, emit func(StreamEvent)) (Message, error) {
	if s == nil || s.repo == nil || s.executor == nil {
		return Message{}, errors.New("assistant turn executor is required")
	}
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return Message{}, err
	}
	if run.SessionID != sessionID || run.Purpose != "assistant" || run.OwnerKind != "environment" || run.Status != agent.RunRunning {
		return Message{}, agent.ErrRunActive
	}
	for {
		history, err := s.repo.ListMessages(ctx, sessionID)
		if err != nil {
			return Message{}, err
		}
		attemptCtx, cancel := context.WithDeadline(ctx, run.DeadlineAt)
		result, err := s.executor.Run(attemptCtx, request, history, emit)
		var message agent.Message
		var completed Message
		if err == nil {
			metadata, encodeErr := json.Marshal(struct {
				Evidence []Evidence `json:"evidence"`
			}{Evidence: result.Evidence})
			if encodeErr != nil {
				err = fmt.Errorf("encode assistant evidence: %w", encodeErr)
			} else {
				now := time.Now().UTC()
				message = agent.Message{
					ID: NewID("assistant-message"), SessionID: sessionID, Role: "assistant", Content: result.Content, Metadata: metadata, CreatedAt: now,
				}
				err = s.repo.CompleteRunWithMessage(attemptCtx, run.ID, message, now)
				if err == nil {
					completed = Message{ID: message.ID, Role: message.Role, Content: message.Content, Evidence: result.Evidence, CreatedAt: now}
				}
			}
		}
		cancel()
		if err == nil {
			return completed, nil
		}
		if ctx.Err() != nil {
			return Message{}, err
		}
		next, retryErr := s.repo.RetryRun(ctx, run.ID, run.Attempt, err.Error(), time.Now().UTC())
		if retryErr != nil {
			return Message{}, retryErr
		}
		if next == nil {
			return Message{}, err
		}
		run = next
	}
}

// RestartInterruptedTurn records the abandoned Server execution and creates a
// replacement Run with the same immutable browser workspace input. The caller
// reconstructs the read-only Environment reader from the current environment.
func (s *Service) RestartInterruptedTurn(ctx context.Context, runID, reason string) (*agent.Run, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("assistant runtime repository is required")
	}
	return s.repo.RestartRun(ctx, runID, reason, time.Now().UTC())
}

// DeleteEnvironment fences a potentially running assistant before an
// environment reset or deletion. Confirmed conversation messages remain
// durable and are never silently deleted by lifecycle cleanup.
func (s *Service) DeleteEnvironment(ctx context.Context, environmentUID string) error {
	if strings.TrimSpace(environmentUID) == "" {
		return nil
	}
	_, err := s.repo.CancelRunsForOwner(ctx, "assistant", "environment", environmentUID, "environment reset or deletion", time.Now().UTC())
	return err
}

func sessionProjection(value *agent.Session, request Request) *Session {
	return &Session{
		ID:              value.ID,
		UserID:          request.UserID,
		EnvironmentUID:  request.EnvironmentUID,
		EnvironmentName: request.EnvironmentName,
		Runtime:         request.Runtime,
		ChallengeID:     request.ChallengeID,
		CreatedAt:       value.CreatedAt,
		UpdatedAt:       value.UpdatedAt,
	}
}

func projectMessages(values []agent.Message) ([]Message, error) {
	result := make([]Message, 0, len(values))
	for _, value := range values {
		message, err := projectMessage(value)
		if err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	return result, nil
}

func projectMessage(value agent.Message) (Message, error) {
	message := Message{ID: value.ID, Role: value.Role, Content: value.Content, CreatedAt: value.CreatedAt}
	if len(value.Metadata) == 0 || bytes.Equal(value.Metadata, []byte("{}")) {
		return message, nil
	}
	var metadata struct {
		Evidence []Evidence `json:"evidence"`
	}
	decoder := json.NewDecoder(bytes.NewReader(value.Metadata))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Message{}, fmt.Errorf("decode assistant message metadata: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Message{}, errors.New("assistant message metadata has a second JSON document")
	} else if !errors.Is(err, io.EOF) {
		return Message{}, fmt.Errorf("decode assistant message metadata suffix: %w", err)
	}
	message.Evidence = metadata.Evidence
	return message, nil
}
