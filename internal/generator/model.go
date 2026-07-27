package generator

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
)

var ErrNotFound = errors.New("generator run not found")

const (
	RuntimePurpose = "generator"
	PromptVersion  = "generator-deep-v4"
	RunDeadline    = time.Hour
)

// RunInput is immutable context for a Generator Run. The plan and the
// verification feedback are loaded by the Server from their authoritative
// Authoring and VerifyTask records; they are not duplicated in agent_runs.
type RunInput struct {
	AuthoringSessionID string   `json:"authoring_session_id"`
	Revision           int64    `json:"revision"`
	SeedSubmissionID   string   `json:"seed_submission_id,omitempty"`
	VerifyTaskID       string   `json:"verify_task_id,omitempty"`
	Feedback           Feedback `json:"feedback"`
}

// Feedback is the structured, actionable output of a prior artifact VerifyTask.
// It is immutable Run input. The Generator sees no raw verifier logs and no
// artifact file content as an additional prompt source.
type Feedback struct {
	BuildPassed       bool    `json:"build_passed"`
	AnswerPassed      bool    `json:"answer_passed"`
	CheckpointsPassed bool    `json:"checkpoints_passed"`
	Summary           string  `json:"summary"`
	Issues            []Issue `json:"issues"`
}

type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f Feedback) Empty() bool {
	return !f.BuildPassed && !f.AnswerPassed && !f.CheckpointsPassed && strings.TrimSpace(f.Summary) == "" && len(f.Issues) == 0
}

func (f Feedback) Validate() error {
	if strings.TrimSpace(f.Summary) == "" && len(f.Issues) == 0 {
		return errors.New("generator verification feedback requires a summary or issue")
	}
	for _, issue := range f.Issues {
		if strings.TrimSpace(issue.Code) == "" || strings.TrimSpace(issue.Message) == "" {
			return errors.New("generator verification feedback issues require code and message")
		}
	}
	return nil
}

type ExecutionContext struct {
	Plan     authoring.Plan `json:"plan"`
	Feedback Feedback       `json:"feedback"`
}

func DecodeRunInput(raw []byte) (RunInput, error) {
	var value RunInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return RunInput{}, fmt.Errorf("decode generator run input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return RunInput{}, errors.New("generator run input contains a second JSON document")
		}
		return RunInput{}, fmt.Errorf("decode generator run input suffix: %w", err)
	}
	if strings.TrimSpace(value.AuthoringSessionID) == "" || value.Revision < 0 {
		return RunInput{}, errors.New("generator run input requires authoring session and revision")
	}
	if !value.Feedback.Empty() {
		if err := value.Feedback.Validate(); err != nil {
			return RunInput{}, err
		}
	}
	return value, nil
}

// Record is the durable Authoring-domain binding for one Agent Run. It tracks
// only workflow facts needed for recovery and deterministic submission, never
// model reasoning, filesystem snapshots, or tool results.
type Record struct {
	RunID                string
	GeneratorSessionID   string
	AuthoringSessionID   string
	AuthoringRevision    int64
	SeedSubmissionID     string
	VerifyTaskID         string
	WorkspaceInitialized bool
	SubmissionID         string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (r Record) RunInput() RunInput {
	return RunInput{
		AuthoringSessionID: r.AuthoringSessionID,
		Revision:           r.AuthoringRevision,
		SeedSubmissionID:   r.SeedSubmissionID,
	}
}

func NewRunID() string { return agentruntime.NewID("generator-run") }

func NewSessionID() string { return agentruntime.NewID("generator-session") }

// SubmissionID is deterministic for one Generator Run. It is never model
// input, and makes an interrupted Server handoff safe to retry without an
// outbox or a duplicate VerifyTask.
func SubmissionID(runID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(runID)))
	return "sub-" + fmt.Sprintf("%x", sum[:12])
}
