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

	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

var ErrArchiveConflict = errors.New("candidate archive conflicts with the immutable stored archive")

func ArchivePath(root, id string) string {
	return filepath.Join(root, "candidates", id, "candidate.tar.gz")
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
