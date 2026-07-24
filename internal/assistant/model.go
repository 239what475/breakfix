package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
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
	AgentSessionID  string    `json:"-"`
	AgentStarted    bool      `json:"-"`
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
	Window     string   `json:"window"`
	Offset     int      `json:"offset"`
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"`
	HasMore    bool     `json:"has_more"`
}

type EnvironmentFile struct {
	Path       string `json:"path"`
	Offset     int64  `json:"offset"`
	Content    string `json:"content"`
	Size       int64  `json:"size"`
	NextOffset int64  `json:"next_offset"`
	HasMore    bool   `json:"has_more"`
}

type EnvironmentFiles struct {
	Path    string   `json:"path"`
	Offset  int      `json:"offset"`
	Entries []string `json:"entries"`
	Total   int      `json:"total"`
	HasMore bool     `json:"has_more"`
}

// Reader is the assistant's complete view of the current environment. Each
// method is implemented by Gateway with a fixed, read-only Kubernetes action.
type Reader interface {
	TerminalScrollback(context.Context, string, int, int) (Scrollback, error)
	CheckpointStatus(context.Context) (CheckpointSnapshot, error)
	ListEnvironmentFiles(context.Context, string, int, int) (EnvironmentFiles, error)
	ReadEnvironmentFile(context.Context, string, int64, int) (EnvironmentFile, error)
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
	CurrentWindow    string
	OpenWindows      []string
	EnvironmentPhase string
	IdleTTL          time.Duration
	Checkpoints      CheckpointSnapshot
	Reader           Reader
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

type Repository interface {
	CreateAssistantSession(context.Context, Session) (*Session, error)
	GetAssistantSession(context.Context, string, string, string) (*Session, error)
	ListAssistantMessages(context.Context, string) ([]Message, error)
	AppendAssistantMessage(context.Context, string, Message) error
	SetAssistantAgentStarted(context.Context, string) error
	DeleteAssistantSessionsForEnvironment(context.Context, string) error
	ListAssistantSessions(context.Context) ([]Session, error)
}

func NewID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
