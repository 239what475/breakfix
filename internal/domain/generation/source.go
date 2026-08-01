package generation

import (
	"errors"
	"strings"
)

// Source identifies the immutable input lineage for a GenerationWorkflow.
// Its ref is an aggregate identity, never a filesystem path or artifact URL.
type Source struct {
	Kind SourceKind `json:"kind"`
	Ref  string     `json:"ref"`
}

type SourceKind string

const (
	SourceAuthoring SourceKind = "authoring"
	SourceRelease   SourceKind = "release"
)

func (k SourceKind) Valid() bool {
	return k == SourceAuthoring || k == SourceRelease
}

func (s Source) Valid() bool {
	return s.Kind.Valid() && strings.TrimSpace(s.Ref) != ""
}

func (s Source) Validate() error {
	if !s.Kind.Valid() {
		return errors.New("generation source kind is invalid")
	}
	if strings.TrimSpace(s.Ref) == "" {
		return errors.New("generation source ref is required")
	}
	return nil
}
