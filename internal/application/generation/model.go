package generation

import (
	"errors"
	"strings"
)

const (
	JudgePurpose       = "judge"
	JudgePromptVersion = "generator-judge-v6"
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
