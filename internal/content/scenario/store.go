package scenario

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	contentrevision "github.com/breakfix/breakfix/internal/content/revision"
	"gopkg.in/yaml.v3"
)

var ErrNotFound = errors.New("scenario not found")

type Entry struct {
	ID              string
	RevisionID      string
	SourceSlug      string
	Type            ScenarioType
	Title           string
	Runtime         string
	Description     string
	Tags            []string
	Image           string
	ContentRevision string
	Revision        string
	PublishedAt     time.Time
	Nodes           []Node
	Versions        []Version
	Topology        string
	Initialization  string
	Reproduction    Reproduction
	Checkpoints     []Checkpoint
	Dir             string
}

type Spec struct {
	ID              string       `yaml:"id"`
	RevisionID      string       `yaml:"revision_id,omitempty"`
	SourceSlug      string       `yaml:"source_slug,omitempty"`
	Type            ScenarioType `yaml:"type,omitempty"`
	Title           string       `yaml:"title"`
	Runtime         string       `yaml:"runtime"`
	Description     string       `yaml:"description"`
	Tags            []string     `yaml:"tags,omitempty"`
	Image           string       `yaml:"image"`
	ContentRevision string       `yaml:"content_revision,omitempty"`
	PublishedAt     time.Time    `yaml:"published_at,omitempty"`
	Nodes           []Node       `yaml:"nodes,omitempty"`
	Versions        []Version    `yaml:"versions,omitempty"`
	Topology        string       `yaml:"topology,omitempty"`
	Initialization  string       `yaml:"initialization,omitempty"`
	Reproduction    Reproduction `yaml:"reproduction,omitempty"`
	Checkpoints     []Checkpoint `yaml:"checkpoints"`
}

// ValidRevision validates a portable content or materialization digest.
func ValidRevision(value string) bool {
	return contentRevisionPattern.MatchString(strings.TrimSpace(value))
}

type Node struct {
	Name  string `yaml:"name" json:"name"`
	Title string `yaml:"title" json:"title"`
}

// Version pins one software, image, or dataset dependency that participates
// in reproducing an operations scenario.
type Version struct {
	Component string `yaml:"component" json:"component"`
	Version   string `yaml:"version" json:"version"`
}

// Reproduction describes the broken initial state that publication must prove
// before it evaluates an optional reference repair.
type Reproduction struct {
	Objective string                 `yaml:"objective" json:"objective"`
	Evidence  []ReproductionEvidence `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

// ReproductionEvidence is an observable fact proving the target phenomenon.
// Node scenarios execute it on Node; Kubernetes evidence executes from the
// management terminal and therefore leaves Node empty.
type ReproductionEvidence struct {
	ID          string `yaml:"id" json:"id"`
	Description string `yaml:"description" json:"description"`
	Node        string `yaml:"node,omitempty" json:"node,omitempty"`
}

// Checkpoint is a user-visible, independently verifiable scenario outcome.
// It describes a state of the environment, never a prescribed command sequence.
type Checkpoint struct {
	ID          string `yaml:"id" json:"id"`
	Title       string `yaml:"title" json:"title"`
	Description string `yaml:"description" json:"description"`
	Hint        string `yaml:"hint,omitempty" json:"hint,omitempty"`
	Node        string `yaml:"node,omitempty" json:"node,omitempty"`
}

func List(root string) ([]Entry, error) {
	sources, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read scenarios dir: %w", err)
	}

	scenarios := make([]Entry, 0, len(sources))
	for _, source := range sources {
		if !source.IsDir() || source.Name() == "base" || strings.HasPrefix(source.Name(), ".") || !ValidSourceSlug(source.Name()) {
			continue
		}
		revisions, err := os.ReadDir(filepath.Join(root, source.Name()))
		if err != nil {
			return nil, fmt.Errorf("read scenario source directory %q: %w", source.Name(), err)
		}
		for _, revision := range revisions {
			if !revision.IsDir() || strings.HasPrefix(revision.Name(), ".") || !ValidRevisionID(revision.Name()) {
				continue
			}
			scenario, err := ValidateDir(filepath.Join(root, source.Name(), revision.Name()))
			if err != nil {
				return nil, err
			}
			if scenario.SourceSlug != source.Name() || scenario.RevisionID != revision.Name() {
				return nil, fmt.Errorf("scenario materialized path mismatch: directory %q has source_slug %q and revision_id %q", filepath.ToSlash(filepath.Join(source.Name(), revision.Name())), scenario.SourceSlug, scenario.RevisionID)
			}
			scenarios = append(scenarios, *scenario)
		}
	}

	slices.SortFunc(scenarios, func(a, b Entry) int {
		if result := strings.Compare(a.ID, b.ID); result != 0 {
			return result
		}
		return strings.Compare(a.RevisionID, b.RevisionID)
	})
	return scenarios, nil
}

// Get returns one exact immutable materialized scenario revision. The content
// package intentionally has no notion of an active revision; that pointer is
// owned by the durable Scenario lifecycle.
func Get(root, id, revisionID string) (*Entry, error) {
	if !ValidID(id) || !ValidRevisionID(revisionID) {
		return nil, ErrNotFound
	}
	scenarios, err := List(root)
	if err != nil {
		return nil, err
	}
	for i := range scenarios {
		if scenarios[i].ID == id && scenarios[i].RevisionID == revisionID {
			scenario := scenarios[i]
			return &scenario, nil
		}
	}
	return nil, ErrNotFound
}

func LoadDir(dir string) (*Entry, error) {
	spec, err := loadSpec(dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.ID) == "" {
		return nil, fmt.Errorf("scenario id is required")
	}
	if !ValidRevisionID(spec.RevisionID) {
		return nil, fmt.Errorf("scenario revision_id is required")
	}
	entry := entryFromSpec(dir, spec)
	if entry.PublishedAt.IsZero() {
		return nil, fmt.Errorf("scenario published_at is required")
	}
	revision, err := artifactRevision(dir)
	if err != nil {
		return nil, err
	}
	entry.Revision = revision
	return entry, nil
}

// LoadCandidateDir reads an unpublished CandidateRevision directory. Platform
// fields are intentionally absent until ScenarioPublish materializes a copy.
func LoadCandidateDir(dir string) (*Entry, error) {
	spec, err := loadSpec(dir)
	if err != nil {
		return nil, err
	}
	return entryFromSpec(dir, spec), nil
}

func loadSpec(dir string) (*Spec, error) {
	data, err := os.ReadFile(filepath.Join(dir, "scenario.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, "scenario.yaml"), err)
	}

	var spec Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, "scenario.yaml"), err)
	}
	return &spec, nil
}

// artifactRevision covers every regular file in the published scenario
// directory, including the executable bit. A materialized source becomes
// stale when the problem, solution, checkpoint implementation, runtime setup,
// or executable mode changes even if manifest metadata stays identical.
func artifactRevision(dir string) (string, error) {
	revision, err := contentrevision.Directory(dir)
	if err != nil {
		return "", fmt.Errorf("hash scenario artifact: %w", err)
	}
	return revision, nil
}

func entryFromSpec(dir string, spec *Spec) *Entry {
	spec.Runtime = NormalizeRuntime(spec.Runtime)
	spec.Type = NormalizeScenarioType(string(spec.Type))
	tags, err := NormalizeTags(spec.Tags)
	if err != nil {
		// Keep the source values so ValidateCandidateDir and ValidateDir can
		// reject them consistently instead of accidentally treating invalid
		// metadata as an empty tag set.
		tags = append([]string(nil), spec.Tags...)
	}

	return &Entry{
		ID: spec.ID, RevisionID: spec.RevisionID,
		SourceSlug:      spec.SourceSlug,
		Type:            spec.Type,
		Title:           spec.Title,
		Runtime:         spec.Runtime,
		Description:     spec.Description,
		Tags:            tags,
		Image:           spec.Image,
		ContentRevision: spec.ContentRevision,
		PublishedAt:     spec.PublishedAt,
		Nodes:           append([]Node{}, spec.Nodes...),
		Versions:        append([]Version{}, spec.Versions...),
		Topology:        spec.Topology,
		Initialization:  spec.Initialization,
		Reproduction: Reproduction{
			Objective: spec.Reproduction.Objective,
			Evidence:  append([]ReproductionEvidence{}, spec.Reproduction.Evidence...),
		},
		Checkpoints: append([]Checkpoint{}, spec.Checkpoints...),
		Dir:         dir,
	}
}
