package challenge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

var requiredFiles = []string{"challenge.yaml", "Dockerfile", "generate.sh", "problem.md", "solution.md", "checks/checkpoints.sh", "answer.sh"}

// MaterializeWithSlug promotes a published challenge into a human-readable
// source directory while keeping its opaque identity in challenge.yaml.
func MaterializeWithSlug(root, id, sourceSlug string, populate func(dst string) error) (*Entry, error) {
	if !ValidSourceSlug(sourceSlug) {
		return nil, fmt.Errorf("invalid challenge source slug %q", sourceSlug)
	}
	return materialize(root, id, sourceSlug, populate)
}

func materialize(root, id, directoryName string, populate func(dst string) error) (*Entry, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid challenge id %q", id)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create challenges dir: %w", err)
	}

	staging := filepath.Join(root, ".tmp-"+directoryName+"-"+randomSuffix())
	if err := os.MkdirAll(staging, 0755); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}

	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := populate(staging); err != nil {
		return nil, err
	}

	challenge, err := ValidateDir(staging)
	if err != nil {
		return nil, err
	}
	if challenge.ID != id {
		return nil, fmt.Errorf("challenge id mismatch: expected %s got %s", id, challenge.ID)
	}
	if challenge.SourceSlug != directoryName {
		return nil, fmt.Errorf("challenge source slug mismatch: expected %s got %s", directoryName, challenge.SourceSlug)
	}

	target := filepath.Join(root, directoryName)
	if _, err := os.Stat(target); err == nil {
		return nil, fmt.Errorf("challenge %s already exists", id)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat challenge dir: %w", err)
	}

	if err := os.Rename(staging, target); err != nil {
		return nil, fmt.Errorf("promote challenge dir: %w", err)
	}
	promoted = true
	challenge.Dir = target
	return challenge, nil
}

func ValidateDir(dir string) (*Entry, error) {
	challenge, err := LoadDir(dir)
	if err != nil {
		return nil, err
	}
	if !ValidID(challenge.ID) {
		return nil, fmt.Errorf("invalid challenge id %q", challenge.ID)
	}
	if !ValidSourceSlug(challenge.SourceSlug) {
		return nil, fmt.Errorf("invalid challenge source slug %q", challenge.SourceSlug)
	}
	for _, name := range requiredFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("missing %s: %w", name, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s must be a file", name)
		}
	}
	if strings.TrimSpace(challenge.Title) == "" {
		return nil, fmt.Errorf("challenge title is required")
	}
	switch strings.TrimSpace(challenge.Type) {
	case "", TypeScript:
	default:
		return nil, fmt.Errorf("unsupported challenge type %q", challenge.Type)
	}
	switch NormalizeRuntime(challenge.Runtime) {
	case RuntimeContainer, RuntimeVCluster:
	default:
		return nil, fmt.Errorf("unsupported challenge runtime %q", challenge.Runtime)
	}
	challenge.Runtime = NormalizeRuntime(challenge.Runtime)
	switch strings.TrimSpace(challenge.Difficulty) {
	case "easy", "medium", "hard":
	default:
		return nil, fmt.Errorf("challenge difficulty must be easy, medium, or hard")
	}
	if strings.TrimSpace(challenge.Description) == "" {
		return nil, fmt.Errorf("challenge description is required")
	}
	if err := validateCheckpoints(challenge, dir); err != nil {
		return nil, err
	}
	return challenge, nil
}

func ValidateSubmissionDir(dir string) (*Entry, error) {
	challenge, err := LoadSubmissionDir(dir)
	if err != nil {
		return nil, err
	}
	for _, name := range requiredFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("missing %s: %w", name, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s must be a file", name)
		}
	}
	if strings.TrimSpace(challenge.Title) == "" {
		return nil, fmt.Errorf("challenge title is required")
	}
	switch strings.TrimSpace(challenge.Type) {
	case "", TypeScript:
	default:
		return nil, fmt.Errorf("unsupported challenge type %q", challenge.Type)
	}
	switch NormalizeRuntime(challenge.Runtime) {
	case RuntimeContainer, RuntimeVCluster:
	default:
		return nil, fmt.Errorf("unsupported challenge runtime %q", challenge.Runtime)
	}
	challenge.Runtime = NormalizeRuntime(challenge.Runtime)
	switch strings.TrimSpace(challenge.Difficulty) {
	case "easy", "medium", "hard":
	default:
		return nil, fmt.Errorf("challenge difficulty must be easy, medium, or hard")
	}
	if strings.TrimSpace(challenge.Description) == "" {
		return nil, fmt.Errorf("challenge description is required")
	}
	if err := validateCheckpoints(challenge, dir); err != nil {
		return nil, err
	}
	return challenge, nil
}

func validateCheckpoints(challenge *Entry, dir string) error {
	if len(challenge.Checkpoints) == 0 {
		return fmt.Errorf("challenge checkpoints are required")
	}
	known := make(map[string]struct{}, len(challenge.Checkpoints))
	for _, checkpoint := range challenge.Checkpoints {
		id := strings.TrimSpace(checkpoint.ID)
		if id == "" || !ValidID(id) {
			return fmt.Errorf("invalid checkpoint id %q", checkpoint.ID)
		}
		if _, ok := known[id]; ok {
			return fmt.Errorf("duplicate checkpoint id %q", id)
		}
		if strings.TrimSpace(checkpoint.Title) == "" {
			return fmt.Errorf("checkpoint %q title is required", id)
		}
		if strings.TrimSpace(checkpoint.Description) == "" {
			return fmt.Errorf("checkpoint %q description is required", id)
		}
		if checkpoint.Hint != "" {
			path, err := safeChallengePath(dir, checkpoint.Hint)
			if err != nil {
				return fmt.Errorf("checkpoint %q hint: %w", id, err)
			}
			if info, err := os.Stat(path); err != nil || info.IsDir() {
				return fmt.Errorf("checkpoint %q hint is not a file", id)
			}
		}
		known[id] = struct{}{}
	}
	for _, checkpoint := range challenge.Checkpoints {
		for _, dependency := range checkpoint.DependsOn {
			if _, ok := known[dependency]; !ok {
				return fmt.Errorf("checkpoint %q depends on unknown checkpoint %q", checkpoint.ID, dependency)
			}
			if dependency == checkpoint.ID {
				return fmt.Errorf("checkpoint %q cannot depend on itself", checkpoint.ID)
			}
		}
	}
	return nil
}

func ValidID(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range id {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' && i > 0 && i < len(id)-1 {
			continue
		}
		return false
	}
	return true
}

// ValidSourceSlug allows a readable Unicode directory name while rejecting
// path syntax. It is not an identity and must never be used as a lookup key.
func ValidSourceSlug(slug string) bool {
	if slug == "" || strings.HasPrefix(slug, "-") || strings.HasSuffix(slug, "-") {
		return false
	}
	for _, r := range slug {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' {
			continue
		}
		return false
	}
	return true
}

func randomSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
