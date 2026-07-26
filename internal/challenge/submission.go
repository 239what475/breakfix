package challenge

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func SaveSubmission(root, id string, r io.Reader) (string, error) {
	return SaveSubmissionAtomic(root, id, r)
}

// SaveSubmissionAtomic persists a deterministic submission exactly once. A
// concurrent retry observes the existing immutable archive instead of
// truncating or replacing it.
func SaveSubmissionAtomic(root, id string, r io.Reader) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid submission id %q", id)
	}
	dir := filepath.Join(root, "submissions", id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create submission dir: %w", err)
	}
	path := filepath.Join(dir, "input.tar.gz")
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("existing submission artifact is not a regular file")
		}
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat submission artifact: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".input-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary submission artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if _, err := io.Copy(temporary, r); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("write submission artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("close submission artifact: %w", err)
	}
	if err := os.Link(temporaryPath, path); err == nil {
		return path, nil
	} else if !os.IsExist(err) {
		return "", fmt.Errorf("link immutable submission artifact: %w", err)
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("read concurrent submission artifact: %w", err)
	}
	return path, nil
}

func SubmissionPath(root, id string) string {
	return filepath.Join(root, "submissions", id, "input.tar.gz")
}

// RemoveSubmission discards an internal handoff archive once it can no longer
// be published or used as the base of a later verified revision.
func RemoveSubmission(root, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid submission id %q", id)
	}
	err := os.RemoveAll(filepath.Join(root, "submissions", id))
	if err != nil {
		return fmt.Errorf("remove submission: %w", err)
	}
	return nil
}
