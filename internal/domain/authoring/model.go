// Package authoring owns the human-reviewable lifecycle of generated challenges.
package authoring

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
)

var (
	ErrNotFound        = errors.New("authoring session not found")
	ErrVersionConflict = errors.New("authoring plan version conflict")
	ErrInvalidState    = errors.New("authoring session is not in a valid state for this operation")
)

type SessionState string

const (
	StateDraftConversation SessionState = "DraftConversation"
	StateIntentReview      SessionState = "IntentReview"
	StatePublished         SessionState = "Published"
)

type Metadata struct {
	Title       string `json:"title"`
	Difficulty  string `json:"difficulty"`
	Description string `json:"description"`
	Runtime     string `json:"runtime"`
}

type Checkpoint struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Markdown string `json:"markdown"`
	Position int    `json:"position"`
}

// Plan is the author-visible, source-of-truth proposal. It intentionally keeps
// checkpoint details as Markdown rather than forcing every challenge into one
// fixed technical template.
type Plan struct {
	Metadata    Metadata     `json:"metadata"`
	Overview    string       `json:"overview"`
	Checkpoints []Checkpoint `json:"checkpoints"`
}

// VerifiedChallenge is a read-only projection of the actual, verified
// challenge.yaml. It is intentionally distinct from Plan: the latter is the
// author's natural-language intent, while this value is what the generator
// really produced and the candidate pipeline verified.
type VerifiedChallenge struct {
	Metadata    Metadata             `json:"metadata"`
	Checkpoints []VerifiedCheckpoint `json:"checkpoints"`
}

type VerifiedCheckpoint struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Hint        string `json:"hint,omitempty"`
	Node        string `json:"node,omitempty"`
}

func (p Plan) Clone() Plan {
	p.Checkpoints = append([]Checkpoint{}, p.Checkpoints...)
	return p
}

func (p Plan) ValidateForGeneration() error {
	metadata := p.Metadata
	if strings.TrimSpace(metadata.Title) == "" {
		return errors.New("题目标题不能为空")
	}
	if strings.TrimSpace(metadata.Description) == "" {
		return errors.New("题目简介不能为空")
	}
	switch metadata.Difficulty {
	case "easy", "medium", "hard":
	default:
		return errors.New("难度必须是 easy、medium 或 hard")
	}
	if runtime := challenge.NormalizeRuntime(metadata.Runtime); runtime != challenge.RuntimeNode && runtime != challenge.RuntimeK8s {
		return errors.New("运行时必须是 node 或 k8s")
	}
	if strings.TrimSpace(p.Overview) == "" {
		return errors.New("题目概览不能为空")
	}
	if len(p.Checkpoints) == 0 {
		return errors.New("至少需要一个检查点")
	}
	seen := make(map[string]struct{}, len(p.Checkpoints))
	for index, checkpoint := range p.SortedCheckpoints() {
		if strings.TrimSpace(checkpoint.ID) == "" || strings.TrimSpace(checkpoint.Title) == "" || strings.TrimSpace(checkpoint.Markdown) == "" {
			return fmt.Errorf("检查点 %d 的标识、标题和说明都不能为空", index+1)
		}
		if _, ok := seen[checkpoint.ID]; ok {
			return fmt.Errorf("检查点标识 %q 重复", checkpoint.ID)
		}
		seen[checkpoint.ID] = struct{}{}
	}
	return nil
}

func (p Plan) SortedCheckpoints() []Checkpoint {
	checkpoints := append([]Checkpoint{}, p.Checkpoints...)
	slices.SortFunc(checkpoints, func(left, right Checkpoint) int {
		if left.Position == right.Position {
			return strings.Compare(left.ID, right.ID)
		}
		return left.Position - right.Position
	})
	return checkpoints
}

type Revision struct {
	Number              int64     `json:"number"`
	Plan                Plan      `json:"plan"`
	CandidateRevisionID string    `json:"candidate_revision_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type Change struct {
	Kind             string `json:"kind"`
	Summary          string `json:"summary"`
	DifficultyImpact string `json:"difficulty_impact"`
	Revision         int64  `json:"revision"`
}

type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Changes   []Change  `json:"changes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	ID                           string       `json:"id"`
	UserID                       string       `json:"user_id"`
	RuntimeSessionID             string       `json:"-"`
	State                        SessionState `json:"state"`
	CurrentRevision              int64        `json:"current_revision"`
	VisibleRevision              int64        `json:"visible_revision"`
	PublishChallengeID           string       `json:"publish_challenge_id,omitempty"`
	RevisionChallengeID          string       `json:"revision_challenge_id,omitempty"`
	RevisionBaseActiveRevisionID string       `json:"revision_base_active_revision_id,omitempty"`
	LastError                    string       `json:"last_error,omitempty"`
	CreatedAt                    time.Time    `json:"created_at"`
	UpdatedAt                    time.Time    `json:"updated_at"`
}

// Stage is a private, attempt-resumable Plan draft. It becomes a public
// Revision only when the matching Agent Run is successfully finalized.
type Stage struct {
	RunID         string    `json:"run_id"`
	SessionID     string    `json:"session_id"`
	BaseRevision  int64     `json:"base_revision"`
	StageRevision int64     `json:"stage_revision"`
	RunAttempt    int       `json:"run_attempt"`
	Plan          Plan      `json:"plan"`
	Changes       []Change  `json:"changes"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// StageOperation identifies one private Plan mutation. The identity includes
// the stage revision so a replay can return the exact stage produced by the
// original mutation without conflating it with a later change in the same run.
type StageOperation struct {
	ID            string
	RequestDigest string
}

// NewStageOperation creates the deterministic receipt identity for one Plan
// tool invocation. The tool arguments must be a typed value or another value
// with stable JSON encoding; callers must use the same arguments to replay an
// outcome whose database response was lost.
func NewStageOperation(runID string, stageRevision int64, kind string, arguments any) (StageOperation, error) {
	runID = strings.TrimSpace(runID)
	kind = strings.TrimSpace(kind)
	if runID == "" || kind == "" || stageRevision < 0 {
		return StageOperation{}, errors.New("authoring stage operation identity is invalid")
	}
	argumentJSON, err := json.Marshal(arguments)
	if err != nil {
		return StageOperation{}, fmt.Errorf("encode authoring stage operation arguments: %w", err)
	}
	canonical, err := json.Marshal(struct {
		RunID         string          `json:"run_id"`
		StageRevision int64           `json:"stage_revision"`
		Kind          string          `json:"kind"`
		Arguments     json.RawMessage `json:"arguments"`
	}{
		RunID: runID, StageRevision: stageRevision, Kind: kind, Arguments: argumentJSON,
	})
	if err != nil {
		return StageOperation{}, fmt.Errorf("encode authoring stage operation: %w", err)
	}
	digest := sha256.Sum256(canonical)
	value := hex.EncodeToString(digest[:])
	return StageOperation{ID: "authoring-stage-" + value, RequestDigest: value}, nil
}

// CheckpointIDForOperation derives an automatically assigned checkpoint ID
// from the same receipt identity as its enclosing Plan mutation.
func CheckpointIDForOperation(operation StageOperation) string {
	digest := strings.TrimSpace(operation.RequestDigest)
	if len(digest) > 24 {
		digest = digest[:24]
	}
	return "checkpoint-" + digest
}

func NewID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
