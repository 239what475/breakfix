// Package revision defines the canonical identity of a filesystem content tree.
package revision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// File is one regular file in a content tree. Path is slash-separated and
// relative to the tree root.
type File struct {
	Path       string
	Content    []byte
	Executable bool
}

// Directory reads a safe regular-file tree and returns its canonical digest.
// The digest covers byte-sorted relative paths, raw file bytes, and whether a
// file is executable. It intentionally does not normalize YAML or file modes
// beyond the executable bit.
func Directory(root string) (string, error) {
	files, err := ReadDirectory(root)
	if err != nil {
		return "", err
	}
	return Files(files), nil
}

// ReadDirectory reads a content tree without following symlinks.
func ReadDirectory(root string) ([]File, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("stat content root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("content root %q must be a directory, not a symlink", root)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open content root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()

	files := make([]File, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("content path escapes root: %q", path)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("content tree does not allow symlink %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("content tree does not allow non-regular file %s", rel)
		}
		content, err := rootFS.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			return fmt.Errorf("read content file %s: %w", rel, err)
		}
		files = append(files, File{
			Path: rel, Content: content, Executable: info.Mode().Perm()&0o111 != 0,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return sorted(files), nil
}

// Files returns the canonical digest for a set of content files.
func Files(files []File) string {
	files = sorted(files)
	hash := sha256.New()
	for _, file := range files {
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		if file.Executable {
			_, _ = hash.Write([]byte{1})
		} else {
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(file.Content)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func sorted(files []File) []File {
	result := append([]File(nil), files...)
	slices.SortFunc(result, func(left, right File) int { return strings.Compare(left.Path, right.Path) })
	return result
}
