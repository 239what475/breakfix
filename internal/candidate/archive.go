package candidate

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

var ErrArchiveConflict = errors.New("candidate archive conflicts with the immutable stored archive")

func ArchivePath(root, id string) string {
	return filepath.Join(root, "candidates", id, "candidate.tar.gz")
}

func BuildArchivePath(root, candidateID, workflowID string, attempt int64) string {
	return filepath.Join(root, "candidates", candidateID, "builds", fmt.Sprintf("%s-%d.oci.tar", workflowID, attempt))
}

// SaveArchiveAtomic stores exactly one immutable archive for a revision. A
// retry is accepted only when the existing bytes have the same digest.
func SaveArchiveAtomic(root, id string, data []byte) (string, string, error) {
	if !challenge.ValidID(id) {
		return "", "", fmt.Errorf("invalid candidate revision id %q", id)
	}
	if len(data) == 0 {
		return "", "", errors.New("candidate archive is empty")
	}
	digest := Digest(data)
	dir := filepath.Join(root, "candidates", id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fmt.Errorf("create candidate archive directory: %w", err)
	}
	path := ArchivePath(root, id)
	if err := verifyExistingArchive(path, digest); err == nil {
		return path, digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}

	temporary, err := os.CreateTemp(dir, ".candidate-*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("create candidate archive temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if _, err := io.Copy(temporary, bytes.NewReader(data)); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("write candidate archive: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("sync candidate archive: %w", err)
	}
	if err := temporary.Chmod(0o440); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("protect candidate archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", "", fmt.Errorf("close candidate archive: %w", err)
	}
	if err := os.Link(temporaryPath, path); err == nil {
		return path, digest, nil
	} else if !errors.Is(err, os.ErrExist) {
		return "", "", fmt.Errorf("publish candidate archive: %w", err)
	}
	if err := verifyExistingArchive(path, digest); err != nil {
		return "", "", err
	}
	return path, digest, nil
}

func ReadArchive(path, expectedDigest string) ([]byte, error) {
	if strings.TrimSpace(path) == "" || !generation.ValidSHA256(expectedDigest) {
		return nil, errors.New("candidate archive path and digest are required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read candidate archive: %w", err)
	}
	if Digest(data) != expectedDigest {
		return nil, ErrArchiveConflict
	}
	return data, nil
}

// SaveBuildArchiveAtomic persists only the exact output of one fenced build
// attempt. A later attempt gets a different path and therefore cannot be
// overwritten by a delayed worker response.
func SaveBuildArchiveAtomic(root, candidateID, workflowID string, attempt int64, data []byte) (string, string, error) {
	if !challenge.ValidID(candidateID) || !challenge.ValidID(workflowID) || attempt <= 0 {
		return "", "", errors.New("build archive requires valid candidate, workflow, and attempt identities")
	}
	if len(data) == 0 {
		return "", "", errors.New("build archive is empty")
	}
	digest := Digest(data)
	path := BuildArchivePath(root, candidateID, workflowID, attempt)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", "", fmt.Errorf("create build archive directory: %w", err)
	}
	if err := verifyExistingArchive(path, digest); err == nil {
		return path, digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".build-*.tmp")
	if err != nil {
		return "", "", fmt.Errorf("create build archive temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("write build archive: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("sync build archive: %w", err)
	}
	if err := temporary.Chmod(0o440); err != nil {
		_ = temporary.Close()
		return "", "", fmt.Errorf("protect build archive: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", "", fmt.Errorf("close build archive: %w", err)
	}
	if err := os.Link(temporaryPath, path); err == nil {
		return path, digest, nil
	} else if !errors.Is(err, os.ErrExist) {
		return "", "", fmt.Errorf("publish build archive: %w", err)
	}
	if err := verifyExistingArchive(path, digest); err != nil {
		return "", "", err
	}
	return path, digest, nil
}

func RemoveBuildArchives(root, candidateID string) error {
	if !challenge.ValidID(candidateID) {
		return fmt.Errorf("invalid candidate revision id %q", candidateID)
	}
	if err := os.RemoveAll(filepath.Join(root, "candidates", candidateID, "builds")); err != nil {
		return fmt.Errorf("remove candidate build archives: %w", err)
	}
	return nil
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + fmt.Sprintf("%x", sum[:])
}

func verifyExistingArchive(path, expectedDigest string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("candidate archive is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read existing candidate archive: %w", err)
	}
	if Digest(data) != expectedDigest {
		return ErrArchiveConflict
	}
	return nil
}
