package assistant

import (
	"context"
	"errors"
	"sync"

	"github.com/breakfix/breakfix/internal/config"
)

func structLLMConfig() config.LLMConfig { return config.LLMConfig{} }

func testRequest(environmentUID string) Request {
	return Request{
		UserID:           "user-a",
		EnvironmentUID:   environmentUID,
		EnvironmentName:  "environment-a",
		Runtime:          "container",
		ChallengeID:      "cleanup-logs",
		ChallengeTitle:   "Cleanup logs",
		Problem:          "Repair the cleanup script.",
		CurrentWindow:    "shell-1",
		OpenWindows:      []string{"shell-1"},
		EnvironmentPhase: "Ready",
		Checkpoints:      CheckpointSnapshot{Results: []CheckpointResult{{ID: "logs", Passed: false, Summary: "not complete"}}},
		Reader:           &fakeReader{},
	}
}

type fakeReader struct {
	scrollbackCalls int
	checkpointCalls int
	listCalls       int
	readCalls       int
}

func (r *fakeReader) TerminalScrollback(_ context.Context, window string, offset, _ int) (Scrollback, error) {
	r.scrollbackCalls++
	return Scrollback{Window: window, Offset: offset, Lines: []string{"$ ls", "broken"}, TotalLines: 2}, nil
}

func (r *fakeReader) CheckpointStatus(context.Context) (CheckpointSnapshot, error) {
	r.checkpointCalls++
	return CheckpointSnapshot{}, nil
}

func (r *fakeReader) ListEnvironmentFiles(_ context.Context, path string, offset, _ int) (EnvironmentFiles, error) {
	r.listCalls++
	return EnvironmentFiles{Path: path, Offset: offset}, nil
}

func (r *fakeReader) ReadEnvironmentFile(_ context.Context, path string, offset int64, _ int) (EnvironmentFile, error) {
	r.readCalls++
	return EnvironmentFile{Path: path, Offset: offset, Content: "kind: ConfigMap"}, nil
}

func (*fakeReader) Solution(context.Context) (string, error) { return "secret solution", nil }

type memoryRepository struct {
	mu       sync.Mutex
	sessions map[string]Session
	messages map[string][]Message
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{sessions: make(map[string]Session), messages: make(map[string][]Message)}
}

func (r *memoryRepository) CreateAssistantSession(_ context.Context, session Session) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.sessions {
		if existing.UserID == session.UserID && existing.EnvironmentUID == session.EnvironmentUID && existing.ChallengeID == session.ChallengeID {
			return nil, errors.New("unique session conflict")
		}
	}
	r.sessions[session.ID] = session
	return &session, nil
}

func (r *memoryRepository) GetAssistantSession(_ context.Context, userID, environmentUID, challengeID string) (*Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, session := range r.sessions {
		if session.UserID == userID && session.EnvironmentUID == environmentUID && session.ChallengeID == challengeID {
			copy := session
			return &copy, nil
		}
	}
	return nil, ErrNotFound
}

func (r *memoryRepository) ListAssistantMessages(_ context.Context, sessionID string) ([]Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message{}, r.messages[sessionID]...), nil
}

func (r *memoryRepository) AppendAssistantMessage(_ context.Context, sessionID string, message Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages[sessionID] = append(r.messages[sessionID], message)
	return nil
}

func (r *memoryRepository) SetAssistantAgentStarted(_ context.Context, sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[sessionID]
	if !ok {
		return ErrNotFound
	}
	session.AgentStarted = true
	r.sessions[sessionID] = session
	return nil
}

func (r *memoryRepository) DeleteAssistantSessionsForEnvironment(_ context.Context, environmentUID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, session := range r.sessions {
		if session.EnvironmentUID == environmentUID {
			delete(r.sessions, id)
			delete(r.messages, id)
		}
	}
	return nil
}

func (r *memoryRepository) ListAssistantSessions(_ context.Context) ([]Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Session, 0, len(r.sessions))
	for _, session := range r.sessions {
		result = append(result, session)
	}
	return result, nil
}
