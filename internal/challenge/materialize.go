package challenge

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var requiredFiles = []string{"challenge.yaml", "Dockerfile", "verify.sh"}

func Materialize(root, id string, populate func(dst string) error) (*Entry, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid challenge id %q", id)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("create challenges dir: %w", err)
	}

	staging := filepath.Join(root, ".tmp-"+id+"-"+randomSuffix())
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

	target := filepath.Join(root, id)
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
	for _, name := range requiredFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("missing %s: %w", name, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("%s must be a file", name)
		}
	}
	return challenge, nil
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

func randomSuffix() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
