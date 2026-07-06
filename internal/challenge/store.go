package challenge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var ErrNotFound = errors.New("challenge not found")

type Entry struct {
	ID          string
	Title       string
	Type        string
	Runtime     string
	Difficulty  string
	Tags        []string
	Description string
	Image       string
	Dir         string
}

type Spec struct {
	ID          string   `yaml:"id"`
	Title       string   `yaml:"title"`
	Type        string   `yaml:"type"`
	Runtime     string   `yaml:"runtime"`
	Difficulty  string   `yaml:"difficulty"`
	Tags        []string `yaml:"tags"`
	Description string   `yaml:"description"`
	Image       string   `yaml:"image"`
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

		challenge, err := LoadDir(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
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

	if direct, err := LoadDir(filepath.Join(root, id)); err == nil {
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
	return entryFromSpec(dir, spec), nil
}

func LoadSubmissionDir(dir string) (*Entry, error) {
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

func entryFromSpec(dir string, spec *Spec) *Entry {
	if spec.Type == "" {
		spec.Type = TypeScript
	}
	spec.Runtime = NormalizeRuntime(spec.Runtime)
	if spec.Image == "" {
		spec.Image = fmt.Sprintf("breakfix-%s:dev", spec.ID)
	}

	return &Entry{
		ID:          spec.ID,
		Title:       spec.Title,
		Type:        spec.Type,
		Runtime:     spec.Runtime,
		Difficulty:  spec.Difficulty,
		Tags:        append([]string{}, spec.Tags...),
		Description: spec.Description,
		Image:       spec.Image,
		Dir:         dir,
	}
}
