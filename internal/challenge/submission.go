package challenge

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func SaveSubmission(root, id string, r io.Reader) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("invalid submission id %q", id)
	}
	dir := filepath.Join(root, "submissions", id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create submission dir: %w", err)
	}
	path := filepath.Join(dir, "input.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create submission artifact: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", fmt.Errorf("write submission artifact: %w", err)
	}
	return path, nil
}

func SubmissionPath(root, id string) string {
	return filepath.Join(root, "submissions", id, "input.tar.gz")
}

