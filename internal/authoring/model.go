// Package authoring owns the human-reviewable lifecycle of generated challenges.
package authoring

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
)

var (
	ErrNotFound        = errors.New("authoring session not found")
	ErrVersionConflict = errors.New("authoring plan version conflict")
	ErrInvalidState    = errors.New("authoring session is not in a valid state for this operation")
)

type SessionState string

const (
	StateDraftConversation                SessionState = "DraftConversation"
	StateIntentReview                     SessionState = "IntentReview"
	StateGeneratingAndVerifying           SessionState = "GeneratingAndVerifying"
	StateVerificationInfrastructureFailed SessionState = "VerificationInfrastructureFailed"
	StateAwaitingVerifiedReview           SessionState = "AwaitingVerifiedReview"
	StateRevisingAndVerifying             SessionState = "RevisingAndVerifying"
	StatePublishing                       SessionState = "Publishing"
	StatePublished                        SessionState = "Published"
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
// really produced and VerifyTask exercised.
type VerifiedChallenge struct {
	Metadata    Metadata             `json:"metadata"`
	Checkpoints []VerifiedCheckpoint `json:"checkpoints"`
}

type VerifiedCheckpoint struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Hint        string   `json:"hint,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
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
	if runtime := challenge.NormalizeRuntime(metadata.Runtime); runtime != challenge.RuntimeContainer && runtime != challenge.RuntimeVCluster {
		return errors.New("运行时必须是 container 或 vcluster")
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

// Artifact is immutable generator output that has already passed its linked
// VerifyTask. No artifact is author-visible before that point.
type Artifact struct {
	SubmissionID   string `json:"submission_id"`
	Directory      string `json:"directory"`
	GeneratorRunID string `json:"generator_run_id"`
}

// VerificationIssue is an internal diagnosis returned by the real VerifyTask.
// It deliberately contains no raw Pod logs, credentials, or other runtime data.
type VerificationIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type VerificationFailureClass string

const (
	VerificationFailureArtifact       VerificationFailureClass = "artifact"
	VerificationFailureInfrastructure VerificationFailureClass = "infrastructure"
)

// VerificationReport carries the same result dimensions as VerifyTaskStatus,
// but uses the public authoring API's snake_case representation.
type VerificationReport struct {
	Class             VerificationFailureClass `json:"class,omitempty"`
	BuildPassed       bool                     `json:"build_passed"`
	AnswerPassed      bool                     `json:"answer_passed"`
	CheckpointsPassed bool                     `json:"checkpoints_passed"`
	Summary           string                   `json:"summary,omitempty"`
	Issues            []VerificationIssue      `json:"issues,omitempty"`
}

// FailureFeedback is the bounded, actionable context given to the next author
// or generator turn after real verification fails.
func (r *VerificationReport) FailureFeedback() string {
	if r == nil {
		return ""
	}

	lines := []string{
		"真实验证未通过。",
		fmt.Sprintf("- 镜像构建：%s", verificationResult(r.BuildPassed)),
		fmt.Sprintf("- 标准解答：%s", verificationResult(r.AnswerPassed)),
		fmt.Sprintf("- 检查点：%s", verificationResult(r.CheckpointsPassed)),
	}
	if summary := strings.TrimSpace(r.Summary); summary != "" {
		lines = append(lines, "摘要："+summary)
	}
	for _, issue := range r.Issues {
		code := strings.TrimSpace(issue.Code)
		message := strings.TrimSpace(issue.Message)
		if code == "" && message == "" {
			continue
		}
		if code == "" {
			lines = append(lines, "- "+message)
			continue
		}
		if message == "" {
			lines = append(lines, "- ["+code+"]")
			continue
		}
		lines = append(lines, "- ["+code+"] "+message)
	}
	return strings.Join(lines, "\n")
}

func verificationResult(passed bool) string {
	if passed {
		return "通过"
	}
	return "未通过"
}

type Verification struct {
	TaskID      string              `json:"task_id"`
	Phase       string              `json:"phase"`
	Message     string              `json:"message"`
	Report      *VerificationReport `json:"report,omitempty"`
	ChallengeID string              `json:"challenge_id,omitempty"`
}

func (v Verification) Failed() bool {
	return v.Phase == string(breakfixv1.VerifyTaskFailed)
}

func (v Verification) Terminal() bool {
	return v.Phase == string(breakfixv1.VerifyTaskFailed) || v.Phase == string(breakfixv1.VerifyTaskSucceeded)
}

func (v Verification) ReportSummary() string {
	if v.Report != nil {
		return v.Report.Summary
	}
	return v.Message
}

type Revision struct {
	Number       int64         `json:"number"`
	Plan         Plan          `json:"plan"`
	Artifact     *Artifact     `json:"artifact,omitempty"`
	Verification *Verification `json:"verification,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
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
	ID               string `json:"id"`
	UserID           string `json:"user_id"`
	RuntimeSessionID string `json:"-"`
	// GeneratorSessionID is the durable Agent Session for one implementation
	// and repair lineage. It is intentionally distinct from the authoring
	// conversation Session: a confirmed revision gets a Generator Session, and
	// an author-requested revision after verification starts a new lineage.
	GeneratorSessionID string       `json:"-"`
	GeneratorRunID     string       `json:"generator_run_id,omitempty"`
	State              SessionState `json:"state"`
	CurrentRevision    int64        `json:"current_revision"`
	VisibleRevision    int64        `json:"visible_revision"`
	VerifyTaskID       string       `json:"verify_task_id,omitempty"`
	PublishChallengeID string       `json:"publish_challenge_id,omitempty"`
	LastError          string       `json:"last_error,omitempty"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
}

// Stage is a private, attempt-resumable Plan draft. It becomes a public
// Revision only when the matching Agent Run is successfully finalized.
type Stage struct {
	RunID         string    `json:"run_id"`
	SessionID     string    `json:"session_id"`
	BaseRevision  int64     `json:"base_revision"`
	StageRevision int64     `json:"stage_revision"`
	Plan          Plan      `json:"plan"`
	Changes       []Change  `json:"changes"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func NewID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
