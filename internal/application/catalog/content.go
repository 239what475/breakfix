package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
)

// ContentRevision returns the canonical content identity for one portable
// source tree. Relative paths are byte-sorted and every regular file
// contributes its raw bytes and executable bit. YAML is never normalized.
func ContentRevision(root string) (catalogdomain.ContentRevision, error) {
	files, err := readSourceFiles(root)
	if err != nil {
		return "", err
	}
	return contentRevisionForFiles(files), nil
}

func contentRevisionForFiles(files []sourceFile) catalogdomain.ContentRevision {
	files = sortedSourceFiles(files)
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
	return catalogdomain.ContentRevision("sha256:" + hex.EncodeToString(hash.Sum(nil)))
}

type sourceFile struct {
	Path       string
	Content    []byte
	Executable bool
}

func readSourceFiles(root string) ([]sourceFile, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("stat source root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("source root %q must be a directory, not a symlink", root)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open source root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()

	files := make([]sourceFile, 0)
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
			return fmt.Errorf("source path escapes root: %q", path)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source tree does not allow symlink %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("source tree does not allow non-regular file %s", rel)
		}
		content, err := rootFS.ReadFile(filepath.FromSlash(rel))
		if err != nil {
			return fmt.Errorf("read source file %s: %w", rel, err)
		}
		files = append(files, sourceFile{
			Path: rel, Content: content, Executable: info.Mode().Perm()&0o111 != 0,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(left, right sourceFile) int { return strings.Compare(left.Path, right.Path) })
	return files, nil
}

func writeSourceLayer(files []sourceFile) ([]byte, error) {
	var uncompressed bytes.Buffer
	if err := writeSourceTar(&uncompressed, files); err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	if err := writeSourceArchive(&compressed, bytes.NewReader(uncompressed.Bytes())); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func writeSourceArchive(destination io.Writer, source io.Reader) error {
	zw, err := gzip.NewWriterLevel(destination, gzip.BestCompression)
	if err != nil {
		return err
	}
	zw.ModTime = time.Unix(0, 0).UTC()
	zw.OS = 255
	if _, err := io.Copy(zw, source); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return nil
}

func writeSourceArchiveFiles(destination io.Writer, files []sourceFile) error {
	zw, err := gzip.NewWriterLevel(destination, gzip.BestCompression)
	if err != nil {
		return err
	}
	zw.ModTime = time.Unix(0, 0).UTC()
	zw.OS = 255
	if err := writeSourceTar(zw, files); err != nil {
		_ = zw.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return nil
}

func writeSourceTar(destination io.Writer, files []sourceFile) error {
	files = sortedSourceFiles(files)
	tw := tar.NewWriter(destination)
	for _, file := range files {
		mode := int64(0o644)
		if file.Executable {
			mode = 0o755
		}
		header := &tar.Header{
			Name: file.Path, Mode: mode, Size: int64(len(file.Content)), Uid: 0, Gid: 0,
			ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{}, Format: tar.FormatPAX,
		}
		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("write portable source layer header %s: %w", file.Path, err)
		}
		if _, err := tw.Write(file.Content); err != nil {
			return fmt.Errorf("write portable source layer %s: %w", file.Path, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close portable source layer: %w", err)
	}
	return nil
}

func sortedSourceFiles(files []sourceFile) []sourceFile {
	result := append([]sourceFile(nil), files...)
	slices.SortFunc(result, func(left, right sourceFile) int { return strings.Compare(left.Path, right.Path) })
	return result
}
