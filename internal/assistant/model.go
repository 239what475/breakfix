package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

var ErrNotFound = errors.New("assistant session not found")

var (
	ErrTurnNotFound = errors.New("assistant turn not found")
	ErrTurnRunning  = errors.New("assistant is already responding")
)

type Session struct {
	ID              string    `json:"id"`
	UserID          string    `json:"-"`
	EnvironmentUID  string    `json:"-"`
	EnvironmentName string    `json:"-"`
	Runtime         string    `json:"-"`
	ChallengeID     string    `json:"challenge_id"`
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

type TurnStatus string

const (
	TurnRunning   TurnStatus = "running"
	TurnCompleted TurnStatus = "completed"
	TurnFailed    TurnStatus = "failed"
)

// Turn is an in-memory assistant response. Only a completed Message is
// persisted; a running Turn exists so a browser can reconnect to its stream.
type Turn struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Status    TurnStatus `json:"status"`
	Content   string     `json:"content"`
	Evidence  []Evidence `json:"evidence,omitempty"`
	Error     string     `json:"error,omitempty"`
	Message   *Message   `json:"message,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
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
	ChallengeID      string
	ChallengeTitle   string
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

// RunInput is the immutable browser workspace state captured with one user
// message. It lets a replacement Worker rebuild the same tool boundary rather
// than silently substituting a default terminal window after a restart.
type RunInput struct {
	CurrentNode   string            `json:"current_node,omitempty"`
	CurrentWindow string            `json:"current_window"`
	Terminals     []TerminalContext `json:"terminals"`
}

// ExecutionContext is the serializable Server-owned snapshot needed to run an
// Assistant attempt. Kubernetes access remains behind Reader on the Server.
type ExecutionContext struct {
	UserID           string                 `json:"user_id"`
	EnvironmentUID   string                 `json:"environment_uid"`
	EnvironmentName  string                 `json:"environment_name"`
	Runtime          string                 `json:"runtime"`
	ChallengeID      string                 `json:"challenge_id"`
	ChallengeTitle   string                 `json:"challenge_title"`
	Problem          string                 `json:"problem"`
	Nodes            []string               `json:"nodes"`
	CurrentNode      string                 `json:"current_node,omitempty"`
	CurrentWindow    string                 `json:"current_window"`
	Terminals        []TerminalContext      `json:"terminals"`
	EnvironmentPhase string                 `json:"environment_phase"`
	Checkpoints      CheckpointSnapshot     `json:"checkpoints"`
	History          []agentruntime.Message `json:"history"`
}

func (value ExecutionContext) Request(reader Reader) Request {
	return Request{
		UserID:           value.UserID,
		EnvironmentUID:   value.EnvironmentUID,
		EnvironmentName:  value.EnvironmentName,
		Runtime:          value.Runtime,
		ChallengeID:      value.ChallengeID,
		ChallengeTitle:   value.ChallengeTitle,
		Problem:          value.Problem,
		Nodes:            append([]string(nil), value.Nodes...),
		CurrentNode:      value.CurrentNode,
		CurrentWindow:    value.CurrentWindow,
		Terminals:        cloneTerminalContexts(value.Terminals),
		EnvironmentPhase: value.EnvironmentPhase,
		Checkpoints:      value.Checkpoints,
		Reader:           reader,
	}
}

func cloneTerminalContexts(values []TerminalContext) []TerminalContext {
	result := make([]TerminalContext, len(values))
	for index := range values {
		result[index] = TerminalContext{Node: values[index].Node, Windows: append([]string(nil), values[index].Windows...)}
	}
	return result
}

// LeaseCredential is the minimum attempt-scoped authority exposed on the
// internal HTTP boundary. The Server resolves the remaining Run fields and
// validates this credential before every environment access or delta publish.
type LeaseCredential = agentruntime.LeaseCredential

type InternalToolRequest struct {
	LeaseCredential
	Arguments json.RawMessage `json:"arguments"`
}

type InternalEventRequest struct {
	LeaseCredential
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Tool    string `json:"tool,omitempty"`
}

type Event struct {
	Type      string   `json:"type"`
	TurnID    string   `json:"turn_id,omitempty"`
	Content   string   `json:"content,omitempty"`
	Tool      string   `json:"tool,omitempty"`
	Turn      *Turn    `json:"turn,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Message   *Message `json:"message,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func NewID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
