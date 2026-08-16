package generation

import (
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/generation"
)

const (
	JudgePurpose            = "judge"
	ClassifierPurpose       = "classifier"
	JudgePromptVersion      = "generator-judge-v6"
	ClassifierPromptVersion = "classification-v1"
)

type FileReadResponse struct {
	Content string `json:"content"`
}

type ArchiveResponse struct {
	Archive []byte `json:"archive"`
}

type ExecuteEvent struct {
	Type     string `json:"type"`
	Content  string `json:"content,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// WorkspaceCommand is the provider-neutral payload carried by the shared tool
// result envelope. A non-zero exit code is a known command failure; a missing
// payload means the provider could not establish whether the command ran.
type WorkspaceCommand struct {
	WorkflowID string `json:"workflow_id"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
}

type Judgement struct {
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback,omitempty"`
}

func (j Judgement) Validate() error {
	if j.Approved && strings.TrimSpace(j.Feedback) != "" {
		return errors.New("approved judgement must not include feedback")
	}
	if !j.Approved && strings.TrimSpace(j.Feedback) == "" {
		return errors.New("rejected judgement requires feedback")
	}
	return nil
}

// ClassificationCompletion is the one terminal result of a Classifying Agent
// run. Initial classification creates a proposal; later feedback either
// updates that same private proposal or routes the feedback to Authoring.
type ClassificationCompletion struct {
	Initial    *generation.ClassificationOutput     `json:"initial,omitempty"`
	Adjustment *generation.ClassificationAdjustment `json:"adjustment,omitempty"`
}

func (r ClassificationCompletion) Validate() error {
	if (r.Initial == nil) == (r.Adjustment == nil) {
		return errors.New("classification completion requires exactly one result")
	}
	if r.Initial != nil {
		return r.Initial.Validate()
	}
	return r.Adjustment.Validate()
}
