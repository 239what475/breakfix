package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
)

var ErrNotFound = errors.New("assistant session not found")

var (
	ErrTurnRunning = errors.New("assistant is already responding")
)

type Session struct {
	ID              string    `json:"id"`
	UserID          string    `json:"-"`
	EnvironmentUID  string    `json:"-"`
	EnvironmentName string    `json:"-"`
	Runtime         string    `json:"-"`
	ScenarioID      string    `json:"scenario_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Evidence struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type Message struct {
	ID        string     `json:"id"`
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

type CheckpointResult struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary"`
	Details string `json:"details,omitempty"`
}

type CheckpointSnapshot struct {
	CheckedAt *time.Time         `json:"checked_at,omitempty"`
	Error     string             `json:"error,omitempty"`
	Results   []CheckpointResult `json:"results"`
}

type Scrollback struct {
	Node       string   `json:"node,omitempty"`
	Window     string   `json:"window"`
	Offset     int      `json:"offset"`
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"`
	HasMore    bool     `json:"has_more"`
}

type EnvironmentFile struct {
	Node       string `json:"node,omitempty"`
	Path       string `json:"path"`
	Offset     int64  `json:"offset"`
	Content    string `json:"content"`
	Size       int64  `json:"size"`
	NextOffset int64  `json:"next_offset"`
	HasMore    bool   `json:"has_more"`
}

type EnvironmentFiles struct {
	Node    string   `json:"node,omitempty"`
	Path    string   `json:"path"`
	Offset  int      `json:"offset"`
	Entries []string `json:"entries"`
	Total   int      `json:"total"`
	HasMore bool     `json:"has_more"`
}

type TerminalContext struct {
	Node    string   `json:"node,omitempty"`
	Windows []string `json:"windows"`
}

// Reader is the assistant's complete view of the current environment. Each
// method is implemented by Server with a fixed, read-only provider action.
type Reader interface {
	TerminalScrollback(context.Context, string, string, int, int) (Scrollback, error)
	CheckpointStatus(context.Context) (CheckpointSnapshot, error)
	ListEnvironmentFiles(context.Context, string, string, int, int) (EnvironmentFiles, error)
	ReadEnvironmentFile(context.Context, string, string, int64, int) (EnvironmentFile, error)
	Solution(context.Context) (string, error)
}

type Request struct {
	UserID           string
	EnvironmentUID   string
	EnvironmentName  string
	Runtime          string
	ScenarioID       string
	ScenarioTitle    string
	Problem          string
	Nodes            []string
	CurrentNode      string
	CurrentWindow    string
	Terminals        []TerminalContext
	EnvironmentPhase string
	IdleTTL          time.Duration
	Checkpoints      CheckpointSnapshot
	Reader           Reader
}

type EngineResult struct {
	Content  string
	Evidence []Evidence
}

// Executor is the model-facing port for a single assistant turn. Its adapter
// owns Eino and transport retries; the application owns durable turn state.
type Executor interface {
	Run(context.Context, Request, []agent.Message, func(StreamEvent)) (EngineResult, error)
}

func ValidateRequest(request Request) error {
	if strings.TrimSpace(request.UserID) == "" || strings.TrimSpace(request.EnvironmentUID) == "" || strings.TrimSpace(request.ScenarioID) == "" {
		return errors.New("assistant session requires user, environment, and scenario")
	}
	if request.Reader == nil {
		return errors.New("assistant reader is required")
	}
	return nil
}

// RunInput is the immutable browser workspace state captured with one user
// message. Server uses it to construct this turn's read-only tool boundary.
type RunInput struct {
	CurrentNode   string            `json:"current_node,omitempty"`
	CurrentWindow string            `json:"current_window"`
	Terminals     []TerminalContext `json:"terminals"`
}

func cloneTerminalContexts(values []TerminalContext) []TerminalContext {
	result := make([]TerminalContext, len(values))
	for index := range values {
		result[index] = TerminalContext{Node: values[index].Node, Windows: append([]string(nil), values[index].Windows...)}
	}
	return result
}

// StreamEvent is emitted while a direct Server-owned assistant turn is
// running. Conversation messages remain durable only when the model call
// completes successfully.
type StreamEvent struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Tool    string `json:"tool,omitempty"`
}

func NewID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
