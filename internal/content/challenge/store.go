package challenge

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

var ErrNotFound = errors.New("challenge not found")

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

// Checkpoint is a user-visible, independently verifiable challenge outcome.
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
		return nil, fmt.Errorf("read challenges dir: %w", err)
	}

	challenges := make([]Entry, 0, len(sources))
	for _, source := range sources {
		if !source.IsDir() || source.Name() == "base" || strings.HasPrefix(source.Name(), ".") || !ValidSourceSlug(source.Name()) {
			continue
		}
		revisions, err := os.ReadDir(filepath.Join(root, source.Name()))
		if err != nil {
			return nil, fmt.Errorf("read challenge source directory %q: %w", source.Name(), err)
		}
		for _, revision := range revisions {
			if !revision.IsDir() || strings.HasPrefix(revision.Name(), ".") || !ValidRevisionID(revision.Name()) {
				continue
			}
			challenge, err := ValidateDir(filepath.Join(root, source.Name(), revision.Name()))
			if err != nil {
				return nil, err
			}
			if challenge.SourceSlug != source.Name() || challenge.RevisionID != revision.Name() {
				return nil, fmt.Errorf("challenge materialized path mismatch: directory %q has source_slug %q and revision_id %q", filepath.ToSlash(filepath.Join(source.Name(), revision.Name())), challenge.SourceSlug, challenge.RevisionID)
			}
			challenges = append(challenges, *challenge)
		}
	}

	slices.SortFunc(challenges, func(a, b Entry) int {
		if result := strings.Compare(a.ID, b.ID); result != 0 {
			return result
		}
		return strings.Compare(a.RevisionID, b.RevisionID)
	})
	return challenges, nil
}

// Get returns one exact immutable materialized challenge revision. The content
// package intentionally has no notion of an active revision; that pointer is
// owned by the durable Challenge lifecycle.
func Get(root, id, revisionID string) (*Entry, error) {
	if !ValidID(id) || !ValidRevisionID(revisionID) {
		return nil, ErrNotFound
	}
	challenges, err := List(root)
	if err != nil {
		return nil, err
	}
	for i := range challenges {
		if challenges[i].ID == id && challenges[i].RevisionID == revisionID {
			challenge := challenges[i]
			return &challenge, nil
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
		return nil, fmt.Errorf("challenge id is required")
	}
	if !ValidRevisionID(spec.RevisionID) {
		return nil, fmt.Errorf("challenge revision_id is required")
	}
	entry := entryFromSpec(dir, spec)
	if entry.PublishedAt.IsZero() {
		return nil, fmt.Errorf("challenge published_at is required")
	}
	revision, err := artifactRevision(dir)
	if err != nil {
		return nil, err
	}
	entry.Revision = revision
	return entry, nil
}

// LoadCandidateDir reads an unpublished CandidateRevision directory. Platform
// fields are intentionally absent until ChallengePublish materializes a copy.
func LoadCandidateDir(dir string) (*Entry, error) {
	spec, err := loadSpec(dir)
	if err != nil {
		return nil, err
	}
	return entryFromSpec(dir, spec), nil
}

func loadSpec(dir string) (*Spec, error) {
	data, err := os.ReadFile(filepath.Join(dir, "challenge.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(dir, "challenge.yaml"), err)
	}

	var spec Spec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, "challenge.yaml"), err)
	}
	return &spec, nil
}

// artifactRevision covers every regular file in the published challenge
// directory, including the executable bit. A materialized source becomes
// stale when the problem, solution, checkpoint implementation, runtime setup,
// or executable mode changes even if manifest metadata stays identical.
func artifactRevision(dir string) (string, error) {
	revision, err := contentrevision.Directory(dir)
	if err != nil {
		return "", fmt.Errorf("hash challenge artifact: %w", err)
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
		Checkpoints:     append([]Checkpoint{}, spec.Checkpoints...),
		Dir:             dir,
	}
}
