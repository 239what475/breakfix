package workspacearchive

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const snapshotRootDirectory = "generator-snapshots"

// Reference is one durable workflow pointer that protects a snapshot from
// garbage collection.
type Reference struct {
	WorkflowID string
	Digest     string
}

// Store persists immutable workspace archives beneath the Server data PVC.
// The database remains the source of truth for references; this store only
// owns bytes and never mutates a published snapshot.
type Store struct {
	root string
}

func NewStore(dataDir string) (*Store, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("workspace snapshot data directory is required")
	}
	return &Store{root: filepath.Join(dataDir, snapshotRootDirectory)}, nil
}

func (s *Store) Path(workflowID, digest string) (string, error) {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return "", errors.New("workspace snapshot store is not configured")
	}
	if !safeSegment(workflowID) || !validDigest(digest) {
		return "", errors.New("workspace snapshot workflow id and digest are required")
	}
	return filepath.Join(s.root, workflowID, strings.TrimPrefix(digest, "sha256:")+".tar.gz"), nil
}

// Save canonicalizes and atomically publishes one immutable snapshot. The
// returned archive is the exact canonical bytes whose digest was published.
func (s *Store) Save(workflowID string, archive []byte) ([]byte, string, error) {
	canonical, err := Canonicalize(archive)
	if err != nil {
		return nil, "", fmt.Errorf("canonicalize workspace snapshot: %w", err)
	}
	digest := Digest(canonical)
	finalPath, err := s.Path(workflowID, digest)
	if err != nil {
		return nil, "", err
	}
	if err := verify(finalPath, digest); err == nil {
		return canonical, digest, nil
	} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, ErrArchiveConflict) {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o750); err != nil {
		return nil, "", fmt.Errorf("create workspace snapshot directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(finalPath), ".snapshot-*.tmp")
	if err != nil {
		return nil, "", fmt.Errorf("create workspace snapshot temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if _, err := io.Copy(temporary, bytes.NewReader(canonical)); err != nil {
		_ = temporary.Close()
		return nil, "", fmt.Errorf("write workspace snapshot: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, "", fmt.Errorf("sync workspace snapshot: %w", err)
	}
	if err := temporary.Chmod(0o440); err != nil {
		_ = temporary.Close()
		return nil, "", fmt.Errorf("protect workspace snapshot: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, "", fmt.Errorf("close workspace snapshot: %w", err)
	}
	if err := verify(temporaryPath, digest); err != nil {
		return nil, "", fmt.Errorf("verify workspace snapshot temporary file: %w", err)
	}
	// A digest collision with different bytes can only be on-disk corruption:
	// the canonical bytes are the digest preimage. Replacing that invalid slot
	// repairs the immutable logical snapshot without creating a new identity.
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		return nil, "", fmt.Errorf("publish workspace snapshot: %w", err)
	}
	return canonical, digest, nil
}

// Read verifies both the stored digest and the canonical archive shape before
// returning bytes suitable for a new Sandbox workspace.
func (s *Store) Read(workflowID, digest string) ([]byte, error) {
	path, err := s.Path(workflowID, digest)
	if err != nil {
		return nil, err
	}
	if err := verify(path, digest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrArchiveNotFound
		}
		return nil, err
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workspace snapshot: %w", err)
	}
	canonical, err := Canonicalize(archive)
	if err != nil {
		return nil, fmt.Errorf("validate workspace snapshot: %w", err)
	}
	if !bytes.Equal(canonical, archive) {
		return nil, ErrArchiveConflict
	}
	return archive, nil
}

// Cleanup removes old temporary files and snapshots not referenced by any
// workflow. The grace deadline protects the rename-to-DB-publication window.
func (s *Store) Cleanup(references []Reference, before time.Time) error {
	if s == nil || strings.TrimSpace(s.root) == "" {
		return errors.New("workspace snapshot store is not configured")
	}
	protected := make(map[string]struct{}, len(references))
	for _, reference := range references {
		path, err := s.Path(reference.WorkflowID, reference.Digest)
		if err == nil {
			protected[path] = struct{}{}
		}
	}
	workflowDirectories, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list workspace snapshots: %w", err)
	}
	for _, directory := range workflowDirectories {
		if !directory.IsDir() || !safeSegment(directory.Name()) {
			continue
		}
		root := filepath.Join(s.root, directory.Name())
		entries, err := os.ReadDir(root)
		if err != nil {
			return fmt.Errorf("list workspace snapshot directory: %w", err)
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.ModTime().After(before) {
				continue
			}
			candidate := filepath.Join(root, entry.Name())
			if _, found := protected[candidate]; found {
				continue
			}
			if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove unreferenced workspace snapshot: %w", err)
			}
		}
		if entries, err := os.ReadDir(root); err == nil && len(entries) == 0 {
			_ = os.Remove(root)
		}
	}
	return nil
}

func verify(path, expectedDigest string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("workspace snapshot is not a regular file")
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read workspace snapshot: %w", err)
	}
	if Digest(archive) != expectedDigest {
		return ErrArchiveConflict
	}
	return nil
}

func safeSegment(value string) bool {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-') {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
