package runtime

import (
	"errors"
	"strings"
)

type FailureClass string

const (
	FailureArtifact       FailureClass = "artifact"
	FailureInfrastructure FailureClass = "infrastructure"
)

type Failure struct {
	Class   FailureClass `json:"class"`
	Code    string       `json:"code"`
	Summary string       `json:"summary"`
}

func (f Failure) Validate() error {
	if (f.Class != FailureArtifact && f.Class != FailureInfrastructure) || strings.TrimSpace(f.Code) == "" || strings.TrimSpace(f.Summary) == "" {
		return errors.New("runtime failure is incomplete")
	}
	return nil
}
