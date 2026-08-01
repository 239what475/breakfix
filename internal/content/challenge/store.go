package challenge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var ErrNotFound = errors.New("challenge not found")

type Entry struct {
	ID              string
	SourceSlug      string
	Title           string
	Runtime         string
	Difficulty      string
	Description     string
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
	SourceSlug      string       `yaml:"source_slug,omitempty"`
	Title           string       `yaml:"title"`
	Runtime         string       `yaml:"runtime"`
	Difficulty      string       `yaml:"difficulty"`
	Description     string       `yaml:"description"`
	Image           string       `yaml:"image"`
	ContentRevision string       `yaml:"content_revision,omitempty"`
	PublishedAt     time.Time    `yaml:"published_at,omitempty"`
	Nodes           []Node       `yaml:"nodes,omitempty"`
	Checkpoints     []Checkpoint `yaml:"checkpoints"`
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
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read challenges dir: %w", err)
	}

	challenges := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "base" || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		challenge, err := ValidateDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		if challenge.SourceSlug != entry.Name() {
			return nil, fmt.Errorf("challenge source slug mismatch: directory %q has %q", entry.Name(), challenge.SourceSlug)
		}
		challenges = append(challenges, *challenge)
	}

	slices.SortFunc(challenges, func(a, b Entry) int {
		return strings.Compare(a.ID, b.ID)
	})
	return challenges, nil
}

func Get(root, id string) (*Entry, error) {
	if id == "" {
		return nil, ErrNotFound
	}

	if direct, err := ValidateDir(filepath.Join(root, id)); err == nil {
		if direct.SourceSlug != id {
			return nil, ErrNotFound
		}
		if direct.ID == id {
			return direct, nil
		}
	}

	challenges, err := List(root)
	if err != nil {
		return nil, err
	}
	for i := range challenges {
		if challenges[i].ID == id {
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
// directory, not only challenge.yaml. Taxonomy mappings therefore become stale
// when the problem, solution, checkpoint implementation, or runtime setup
// changes even if manifest metadata stays identical.
func artifactRevision(dir string) (string, error) {
	files := make([]string, 0)
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("challenge revision does not allow non-regular file %s", path)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk challenge artifact for revision: %w", err)
	}
	slices.Sort(files)
	hash := sha256.New()
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", fmt.Errorf("read challenge artifact for revision: %w", err)
		}
		_, _ = hash.Write([]byte(rel))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func entryFromSpec(dir string, spec *Spec) *Entry {
	spec.Runtime = NormalizeRuntime(spec.Runtime)

	return &Entry{
		ID:              spec.ID,
		SourceSlug:      spec.SourceSlug,
		Title:           spec.Title,
		Runtime:         spec.Runtime,
		Difficulty:      spec.Difficulty,
		Description:     spec.Description,
		Image:           spec.Image,
		ContentRevision: spec.ContentRevision,
		PublishedAt:     spec.PublishedAt,
		Nodes:           append([]Node{}, spec.Nodes...),
		Checkpoints:     append([]Checkpoint{}, spec.Checkpoints...),
		Dir:             dir,
	}
}
