// Package agent owns provider-neutral model sessions, messages, and calls.
// It intentionally contains no scheduling or provider implementation.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound  = errors.New("agent runtime record not found")
	ErrRunActive = errors.New("agent session already has an active run")
)

type SessionStatus string

const (
	SessionActive SessionStatus = "active"
	SessionClosed SessionStatus = "closed"
)

type RunStatus string

const (
	RunRunning     RunStatus = "running"
	RunSucceeded   RunStatus = "succeeded"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
	RunInterrupted RunStatus = "interrupted"
)

// MaxAttempts is the bounded retry budget used by short, typed agent tasks
// such as Judge, Classifier, and Roadmap. Interactive Authoring instead uses
// its own deadline as the only turn budget.
const MaxAttempts = 5

type Session struct {
	ID        string        `json:"id"`
	Purpose   string        `json:"purpose"`
	OwnerKind string        `json:"owner_kind"`
	OwnerRef  string        `json:"owner_ref"`
	UserRef   string        `json:"user_ref,omitempty"`
	Status    SessionStatus `json:"status"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type Message struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Sequence  int64           `json:"sequence"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type Run struct {
	ID            string          `json:"id"`
	SessionID     string          `json:"session_id,omitempty"`
	Purpose       string          `json:"purpose"`
	OwnerKind     string          `json:"owner_kind"`
	OwnerRef      string          `json:"owner_ref"`
	InputRevision string          `json:"input_revision,omitempty"`
	Input         json.RawMessage `json:"input"`
	Status        RunStatus       `json:"status"`
	Model         string          `json:"model"`
	PromptVersion string          `json:"prompt_version"`
	Attempt       int             `json:"attempt"`
	DeadlineAt    time.Time       `json:"deadline_at"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
}

type CreateRun struct {
	ID            string
	SessionID     string
	Purpose       string
	OwnerKind     string
	OwnerRef      string
	InputRevision string
	Input         json.RawMessage
	Model         string
	PromptVersion string
	// DeadlineAt is an execution bound for this logical run. A zero value lets
	// the repository apply its standard bounded deadline.
	DeadlineAt time.Time
}

// Repository is deliberately limited to durable conversation data. Workflow
// repositories expose their own claim/report methods in the owning package.
type Repository interface {
	CreateSession(context.Context, Session) (*Session, error)
	FindOrCreateSession(context.Context, Session) (*Session, error)
	FindSession(context.Context, string, string, string, string) (*Session, error)
	GetSession(context.Context, string) (*Session, error)
	ListMessages(context.Context, string) ([]Message, error)
	CreateMessageAndRun(context.Context, Message, CreateRun) (*Run, error)
	CreateRun(context.Context, CreateRun) (*Run, error)
	GetRun(context.Context, string) (*Run, error)
	GetActiveRunForSession(context.Context, string) (*Run, error)
	ListRunsForOwner(context.Context, string, string) ([]Run, error)
	CompleteRunWithMessage(context.Context, string, Message, time.Time) error
	CompleteRun(context.Context, string, time.Time) error
	FailRun(context.Context, string, string, time.Time) error
	RetryRun(context.Context, string, int, string, time.Time) (*Run, error)
	InterruptRun(context.Context, string, string, time.Time) error
	RestartRun(context.Context, string, string, time.Time) (*Run, error)
	CancelRunsForOwner(context.Context, string, string, string, string, time.Time) (int64, error)
}

func NewID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}

func ValidateCreateRun(run CreateRun) error {
	if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.Purpose) == "" ||
		strings.TrimSpace(run.OwnerKind) == "" || strings.TrimSpace(run.OwnerRef) == "" {
		return errors.New("agent run requires id, purpose, owner kind, and owner ref")
	}
	if strings.TrimSpace(run.Model) == "" || strings.TrimSpace(run.PromptVersion) == "" {
		return errors.New("agent run requires model and prompt version")
	}
	if !run.DeadlineAt.IsZero() && !run.DeadlineAt.After(time.Now().UTC()) {
		return errors.New("agent run deadline must be in the future")
	}
	if len(run.Input) > 0 && !json.Valid(run.Input) {
		return errors.New("agent run input must be valid JSON")
	}
	return nil
}
