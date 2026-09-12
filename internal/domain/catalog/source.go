package catalog

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	SourceAPIVersion = "breakfix.dev/catalog/v1"
	SourceKind       = "CatalogRelease"
)

// SourceManifest is the portable release.yaml contract. It deliberately
// identifies candidate source trees rather than target-platform scenarios.
type SourceManifest struct {
	APIVersion string         `yaml:"apiVersion" json:"apiVersion"`
	Kind       string         `yaml:"kind" json:"kind"`
	Metadata   SourceMetadata `yaml:"metadata" json:"metadata"`
	Entries    []SourceEntry  `yaml:"entries" json:"entries"`
}

type SourceMetadata struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
}

// SourceEntry points at one candidate directory inside the release source.
// It has no scenario ID, slug, runtime artifact, or publication time.
type SourceEntry struct {
	Path            string          `yaml:"path" json:"path"`
	ContentRevision ContentRevision `yaml:"contentRevision" json:"contentRevision"`
}

func (m SourceManifest) Validate() error {
	if m.APIVersion != SourceAPIVersion {
		return fmt.Errorf("release apiVersion must be %q", SourceAPIVersion)
	}
	if m.Kind != SourceKind {
		return fmt.Errorf("release kind must be %q", SourceKind)
	}
	if strings.TrimSpace(m.Metadata.Name) == "" || strings.TrimSpace(m.Metadata.Version) == "" {
		return errors.New("release metadata name and version are required")
	}
	if len(m.Entries) == 0 {
		return errors.New("release entries are required")
	}
	seen := make(map[string]struct{}, len(m.Entries))
	for _, entry := range m.Entries {
		if !validScenarioSourcePath(entry.Path) {
			return fmt.Errorf("invalid release scenario path %q", entry.Path)
		}
		if err := entry.ContentRevision.Validate(); err != nil {
			return fmt.Errorf("release entry %q contentRevision: %w", entry.Path, err)
		}
		if _, exists := seen[entry.Path]; exists {
			return fmt.Errorf("duplicate release scenario path %q", entry.Path)
		}
		seen[entry.Path] = struct{}{}
	}
	return nil
}

func validScenarioSourcePath(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "\\") || path.IsAbs(value) {
		return false
	}
	clean := path.Clean(value)
	if clean != value || !strings.HasPrefix(clean, "scenarios/") {
		return false
	}
	return strings.TrimPrefix(clean, "scenarios/") != ""
}
