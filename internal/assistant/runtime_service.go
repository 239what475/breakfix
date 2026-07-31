package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

const (
	maxAssistantTurns    = 12
	assistantRunDeadline = agentruntime.ExecutionDeadline
	promptVersion        = "assistant-v1"
)

type Subscription struct {
	Events <-chan Event
	close  func()
}

func (s *Subscription) Close() {
	if s != nil && s.close != nil {
		s.close()
	}
}

type Service struct {
	repo  agentruntime.Repository
	model string
	hub   *eventHub
}

func NewService(repo agentruntime.Repository, model string) *Service {
	return &Service{repo: repo, model: strings.TrimSpace(model), hub: newEventHub()}
}

func (s *Service) GetOrCreate(ctx context.Context, request Request) (*Session, []Message, error) {
	if err := validateRequest(request); err != nil {
		return nil, nil, err
	}
	if s.repo == nil {
		return nil, nil, errors.New("assistant runtime repository is required")
	}
	session, err := s.repo.FindOrCreateSession(ctx, agentruntime.Session{
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

// StartTurn stores the user message and pending Run in one transaction. Model
// execution is intentionally not started in the Server process.
func (s *Service) StartTurn(ctx context.Context, request Request, content string) (*Session, Turn, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, Turn{}, errors.New("消息不能为空")
	}
	session, _, err := s.GetOrCreate(ctx, request)
	if err != nil {
		return nil, Turn{}, err
	}
	now := time.Now().UTC()
	input, err := json.Marshal(RunInput{CurrentNode: request.CurrentNode, CurrentWindow: request.CurrentWindow, Terminals: cloneTerminalContexts(request.Terminals)})
	if err != nil {
		return nil, Turn{}, fmt.Errorf("encode assistant run input: %w", err)
	}
	run, err := s.repo.CreateMessageAndRun(ctx, agentruntime.Message{
		ID:        NewID("assistant-message"),
		SessionID: session.ID,
		Role:      "user",
		Content:   content,
		CreatedAt: now,
	}, agentruntime.CreateRun{
		ID:               NewID("assistant-run"),
		SessionID:        session.ID,
		Purpose:          "assistant",
		OwnerKind:        "environment",
		OwnerRef:         request.EnvironmentUID,
		InputRevision:    request.EnvironmentUID,
		Input:            input,
		Model:            s.model,
		PromptVersion:    promptVersion,
		ExecutionTimeout: assistantRunDeadline,
	})
	if errors.Is(err, agentruntime.ErrRunActive) {
		return nil, Turn{}, ErrTurnRunning
	}
	if err != nil {
		return nil, Turn{}, err
	}
	return session, turnProjection(run), nil
}

func (s *Service) ActiveTurn(ctx context.Context, sessionID string) *Turn {
	run, err := s.repo.GetActiveRunForSession(ctx, sessionID)
	if err != nil {
		return nil
	}
	turn := turnProjection(run)
	return &turn
}

func (s *Service) Subscribe(ctx context.Context, sessionID, runID string) (*Subscription, error) {
	run, err := s.repo.GetRun(ctx, runID)
	if errors.Is(err, agentruntime.ErrNotFound) || run.SessionID != sessionID {
		return nil, ErrTurnNotFound
	}
	if err != nil {
		return nil, err
	}
	channel, closeHub := s.hub.subscribe(runID)
	cancelWatch := make(chan struct{})
	closeOnce := sync.Once{}
	closeSubscription := func() {
		closeOnce.Do(func() {
			close(cancelWatch)
			closeHub()
		})
	}
	// The subscription owns the watcher lifetime. Its request context may be
	// cancelled when the HTTP handler upgrades to WebSocket, so retain values
	// without inheriting that cancellation.
	go s.watchRun(context.WithoutCancel(ctx), sessionID, runID, channel, cancelWatch)
	s.emitCurrentRunState(ctx, sessionID, run, channel)
	return &Subscription{Events: channel, close: closeSubscription}, nil
}

// Publish is called by the Server internal delta endpoint. Deltas are only
// delivered to current subscribers and are never written to PostgreSQL.
func (s *Service) Publish(runID string, event Event) {
	event.TurnID = runID
	s.hub.publish(runID, event)
}

// DeleteEnvironment fences a potentially running assistant before an
// environment reset or deletion. Confirmed conversation messages remain
// durable and are never silently deleted by lifecycle cleanup.
func (s *Service) DeleteEnvironment(ctx context.Context, environmentUID string) error {
	if strings.TrimSpace(environmentUID) == "" {
		return nil
	}
	_, err := s.repo.CancelRunsForOwner(ctx, "assistant", "environment", environmentUID, time.Now().UTC())
	return err
}

func (s *Service) watchRun(ctx context.Context, sessionID, runID string, channel chan<- Event, stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			run, err := s.repo.GetRun(ctx, runID)
			if err != nil {
				nonBlockingSend(channel, Event{Type: "error", TurnID: runID, Error: "assistant run is unavailable"})
				return
			}
			if isTerminal(run.Status) {
				s.emitCurrentRunState(ctx, sessionID, run, channel)
				return
			}
		}
	}
}

func (s *Service) emitCurrentRunState(ctx context.Context, sessionID string, run *agentruntime.Run, channel chan<- Event) {
	switch run.Status {
	case agentruntime.RunPending, agentruntime.RunRunning:
		turn := turnProjection(run)
		nonBlockingSend(channel, Event{Type: "ready", TurnID: run.ID, SessionID: sessionID, Turn: &turn})
	case agentruntime.RunSucceeded:
		messages, err := s.repo.ListMessages(ctx, sessionID)
		if err != nil {
			nonBlockingSend(channel, Event{Type: "error", TurnID: run.ID, Error: "assistant response is unavailable"})
			return
		}
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].Role != "assistant" {
				continue
			}
			message, err := projectMessage(messages[index])
			if err != nil {
				nonBlockingSend(channel, Event{Type: "error", TurnID: run.ID, Error: "assistant response metadata is unavailable"})
				return
			}
			nonBlockingSend(channel, Event{Type: "complete", TurnID: run.ID, SessionID: sessionID, Message: &message})
			return
		}
		nonBlockingSend(channel, Event{Type: "error", TurnID: run.ID, Error: "assistant run completed without a response"})
	case agentruntime.RunFailed, agentruntime.RunCancelled:
		message := strings.TrimSpace(run.LastError)
		if message == "" {
			message = "assistant response failed"
		}
		nonBlockingSend(channel, Event{Type: "error", TurnID: run.ID, Error: message})
	default:
		nonBlockingSend(channel, Event{Type: "error", TurnID: run.ID, Error: fmt.Sprintf("unknown assistant run state %q", run.Status)})
	}
}

func isTerminal(status agentruntime.RunStatus) bool {
	return status == agentruntime.RunSucceeded || status == agentruntime.RunFailed || status == agentruntime.RunCancelled
}

func sessionProjection(value *agentruntime.Session, request Request) *Session {
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

func turnProjection(value *agentruntime.Run) Turn {
	return Turn{
		ID:        value.ID,
		SessionID: value.SessionID,
		Status:    TurnRunning,
		CreatedAt: value.CreatedAt,
		UpdatedAt: value.UpdatedAt,
	}
}

func projectMessages(values []agentruntime.Message) ([]Message, error) {
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

func projectMessage(value agentruntime.Message) (Message, error) {
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

type eventHub struct {
	mu   sync.Mutex
	next uint64
	runs map[string]map[uint64]chan Event
}

func newEventHub() *eventHub {
	return &eventHub{runs: make(map[string]map[uint64]chan Event)}
}

func (h *eventHub) subscribe(runID string) (chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	channel := make(chan Event, 128)
	if h.runs[runID] == nil {
		h.runs[runID] = make(map[uint64]chan Event)
	}
	h.runs[runID][h.next] = channel
	id := h.next
	return channel, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if subscribers := h.runs[runID]; subscribers != nil {
			delete(subscribers, id)
			if len(subscribers) == 0 {
				delete(h.runs, runID)
			}
		}
	}
}

func (h *eventHub) publish(runID string, event Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, channel := range h.runs[runID] {
		nonBlockingSend(channel, event)
	}
}

func nonBlockingSend(channel chan<- Event, event Event) {
	select {
	case channel <- event:
	default:
	}
}
