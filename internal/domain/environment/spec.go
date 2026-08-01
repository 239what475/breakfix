package environment

import (
	"fmt"
	"strings"
)

type Purpose string

const (
	PurposeLearning     Purpose = "learning"
	PurposeVerification Purpose = "verification"
)

type SourceKind string

const (
	SourcePublished SourceKind = "published"
	SourceCandidate SourceKind = "candidate"
)

type Source struct {
	Kind     SourceKind
	Ref      string
	Revision string
}

type Checkpoint struct {
	ID   string
	Node string
}

// Spec is the provider-neutral portion shared by Node and VK8s environments.
type Spec struct {
	Purpose     Purpose
	Source      Source
	Checkpoints []Checkpoint
}

func (s Spec) Validate() error {
	if s.Purpose != PurposeLearning && s.Purpose != PurposeVerification {
		return fmt.Errorf("unsupported environment purpose %q", s.Purpose)
	}
	if s.Source.Kind != SourcePublished && s.Source.Kind != SourceCandidate {
		return fmt.Errorf("unsupported environment source kind %q", s.Source.Kind)
	}
	if strings.TrimSpace(s.Source.Ref) == "" || strings.TrimSpace(s.Source.Revision) == "" {
		return fmt.Errorf("environment source ref and revision are required")
	}
	if s.Purpose == PurposeLearning && s.Source.Kind != SourcePublished {
		return fmt.Errorf("learning environments require a published source")
	}
	if s.Purpose == PurposeVerification && s.Source.Kind != SourceCandidate {
		return fmt.Errorf("verification environments require a candidate source")
	}
	checkpointIDs := make(map[string]struct{}, len(s.Checkpoints))
	for _, checkpoint := range s.Checkpoints {
		if strings.TrimSpace(checkpoint.ID) == "" {
			return fmt.Errorf("checkpoint ID is required")
		}
		if _, duplicate := checkpointIDs[checkpoint.ID]; duplicate {
			return fmt.Errorf("duplicate checkpoint %q", checkpoint.ID)
		}
		checkpointIDs[checkpoint.ID] = struct{}{}
	}
	if len(checkpointIDs) == 0 {
		return fmt.Errorf("environment checkpoints are required")
	}
	return nil
}
