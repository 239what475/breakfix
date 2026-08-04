package generation

import (
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

const (
	GeneratorPurpose       = "generator"
	JudgePurpose           = "judge"
	ClassifierPurpose      = "classifier"
	GeneratorPromptVersion = "generator-deep-v5"
	JudgePromptVersion     = "generator-judge-v6"
	ClassifierPromptVersion = "classification-v1"
)

type WorkspaceContext struct {
	Plan     authoring.Plan      `json:"plan"`
	Feedback generation.Feedback `json:"feedback"`
}

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
