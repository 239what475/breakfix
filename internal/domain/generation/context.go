package generation

import (
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/authoring"
)

// Feedback is the compact, trusted repair context handed to a later generator
// turn. It never contains raw sandbox output or candidate-controlled text.
type Feedback struct {
	Summary string  `json:"summary"`
	Issues  []Issue `json:"issues"`
}

type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f Feedback) Empty() bool {
	return strings.TrimSpace(f.Summary) == "" && len(f.Issues) == 0
}

func (f Feedback) Validate() error {
	if f.Empty() {
		return nil
	}
	if strings.TrimSpace(f.Summary) == "" || len(f.Issues) == 0 {
		return errors.New("generation feedback requires summary and issues")
	}
	for _, issue := range f.Issues {
		if strings.TrimSpace(issue.Code) == "" || strings.TrimSpace(issue.Message) == "" {
			return errors.New("generation feedback issue requires code and message")
		}
	}
	return nil
}

// Context is the complete Server-owned input for the current workflow state.
// Candidate bytes are intentionally absent and must be fetched through the
// corresponding lease-fenced artifact endpoint.
type Context struct {
	Workflow  Workflow       `json:"workflow"`
	Plan      authoring.Plan `json:"plan"`
	Candidate *Revision      `json:"candidate,omitempty"`
	Feedback  Feedback       `json:"feedback"`
}

// ArtifactError marks a deterministic defect in candidate content or its
// generated verification result. Workers report it as an artifact failure so
// the workflow can return to the same generator session for repair.
type ArtifactError struct {
	Failure Failure
}

func (e *ArtifactError) Error() string { return e.Failure.Summary }

func NewArtifactError(code, summary string) error {
	return &ArtifactError{Failure: Failure{Class: FailureArtifact, Code: code, Summary: summary}}
}
