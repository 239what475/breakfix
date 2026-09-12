// Package workspacearchive defines the canonical, safe archive format used
// for mutable Generator workspaces and immutable candidate submissions.
package workspacearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

var (
	ErrArchiveConflict = errors.New("workspace archive conflicts with its canonical form")
	ErrArchiveNotFound = errors.New("workspace archive not found")
)

var archiveEpoch = time.Unix(0, 0).UTC()

// Entry is one safe workspace tree entry. Paths are slash-separated and
// relative. Only directories and regular files are representable.
type Entry struct {
	Path      string
	Directory bool
	Mode      int
	Content   []byte
}

// Encode returns a deterministic tar.gz archive. It normalizes permissions to
// the only semantic distinction the scenario format preserves: executable or
// non-executable regular files. Parent directories are materialized so a
// decoded archive can be restored without relying on tar extraction behavior.
func Encode(entries []Entry) ([]byte, error) {
	entries, err := normalize(entries)
	if err != nil {
		return nil, err
	}
	var destination bytes.Buffer
	writer, err := gzip.NewWriterLevel(&destination, gzip.BestCompression)
	if err != nil {
		return nil, fmt.Errorf("create workspace archive gzip writer: %w", err)
	}
	writer.ModTime = archiveEpoch
	writer.OS = 255
	tarWriter := tar.NewWriter(writer)
	for _, entry := range entries {
		header := &tar.Header{
			Mode:       int64(entry.Mode),
			Uid:        0,
			Gid:        0,
			ModTime:    archiveEpoch,
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
			Format:     tar.FormatPAX,
		}
		if entry.Directory {
			header.Name = entry.Path + "/"
			header.Typeflag = tar.TypeDir
		} else {
			header.Name = entry.Path
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.Content))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			_ = writer.Close()
			return nil, fmt.Errorf("write workspace archive header %q: %w", entry.Path, err)
		}
		if !entry.Directory {
			if _, err := tarWriter.Write(entry.Content); err != nil {
				_ = tarWriter.Close()
				_ = writer.Close()
				return nil, fmt.Errorf("write workspace archive file %q: %w", entry.Path, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("close workspace archive tar writer: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close workspace archive gzip writer: %w", err)
	}
	return destination.Bytes(), nil
}

// Decode validates an archive and returns its normalized tree. Archive
// metadata is deliberately discarded: it is not a durable workspace semantic.
func Decode(archive []byte) ([]Entry, error) {
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open workspace archive gzip stream: %w", err)
	}
	defer func() { _ = reader.Close() }()

	tarReader := tar.NewReader(reader)
	entries := make([]Entry, 0)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read workspace archive entry: %w", err)
		}
		name, root, err := archivePath(header.Name, header.Typeflag == tar.TypeDir)
		if err != nil {
			return nil, err
		}
		if root {
			if header.Typeflag != tar.TypeDir {
				return nil, fmt.Errorf("workspace archive root is not a directory")
			}
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return nil, fmt.Errorf("workspace archive directory %q has content", header.Name)
			}
			entries = append(entries, Entry{Path: name, Directory: true, Mode: int(header.Mode)})
		case tar.TypeReg, tar.TypeRegA:
			if header.Linkname != "" {
				return nil, fmt.Errorf("workspace archive regular file %q has a link target", header.Name)
			}
			content, err := io.ReadAll(tarReader)
			if err != nil {
				return nil, fmt.Errorf("read workspace archive file %q: %w", header.Name, err)
			}
			entries = append(entries, Entry{Path: name, Mode: int(header.Mode), Content: content})
		default:
			return nil, fmt.Errorf("workspace archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
	}
	entries, err = normalize(entries)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// Canonicalize rejects unsafe content and re-encodes it with deterministic
// metadata. The returned digest is always a digest of the returned bytes.
func Canonicalize(archive []byte) ([]byte, error) {
	entries, err := Decode(archive)
	if err != nil {
		return nil, err
	}
	return Encode(entries)
}

// Extract writes a previously validated archive beneath an existing empty
// directory. It uses an os.Root so archive paths cannot escape the target.
func Extract(destination string, archive []byte) error {
	entries, err := Decode(archive)
	if err != nil {
		return err
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return fmt.Errorf("stat workspace archive destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("workspace archive destination must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("open workspace archive destination: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, entry := range entries {
		if entry.Directory {
			if err := root.MkdirAll(entry.Path, 0o755); err != nil {
				return fmt.Errorf("create workspace archive directory %q: %w", entry.Path, err)
			}
			continue
		}
		parent := path.Dir(entry.Path)
		if parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("create workspace archive parent for %q: %w", entry.Path, err)
			}
		}
		file, err := root.OpenFile(entry.Path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(entry.Mode))
		if err != nil {
			return fmt.Errorf("create workspace archive file %q: %w", entry.Path, err)
		}
		if _, err := file.Write(entry.Content); err != nil {
			_ = file.Close()
			return fmt.Errorf("write workspace archive file %q: %w", entry.Path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close workspace archive file %q: %w", entry.Path, err)
		}
		if err := root.Chmod(entry.Path, os.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("set workspace archive file mode %q: %w", entry.Path, err)
		}
	}
	return nil
}

// Digest is the SHA-256 identity used by snapshot database pointers.
func Digest(archive []byte) string {
	sum := sha256.Sum256(archive)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalize(entries []Entry) ([]Entry, error) {
	byPath := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		name, root, err := archivePath(entry.Path, entry.Directory)
		if err != nil {
			return nil, err
		}
		if root {
			return nil, errors.New("workspace archive entries cannot name the root")
		}
		if entry.Directory && len(entry.Content) != 0 {
			return nil, fmt.Errorf("workspace archive directory %q has content", entry.Path)
		}
		if _, found := byPath[name]; found {
			return nil, fmt.Errorf("workspace archive contains duplicate path %q", name)
		}
		entry.Path = name
		entry.Content = append([]byte(nil), entry.Content...)
		if entry.Directory {
			entry.Mode = 0o755
		} else if entry.Mode&0o111 != 0 {
			entry.Mode = 0o755
		} else {
			entry.Mode = 0o644
		}
		byPath[name] = entry
	}
	for name, entry := range byPath {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if existing, found := byPath[parent]; found {
				if !existing.Directory {
					return nil, fmt.Errorf("workspace archive file %q cannot be a parent of %q", parent, name)
				}
				continue
			}
			byPath[parent] = Entry{Path: parent, Directory: true, Mode: 0o755}
		}
		if entry.Directory {
			continue
		}
	}
	result := make([]Entry, 0, len(byPath))
	for _, entry := range byPath {
		result = append(result, entry)
	}
	slices.SortFunc(result, func(left, right Entry) int {
		return strings.Compare(left.Path, right.Path)
	})
	return result, nil
}

func archivePath(value string, directory bool) (string, bool, error) {
	if directory {
		value = strings.TrimSuffix(value, "/")
	}
	if value == "." || value == "" {
		return "", true, nil
	}
	if strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", false, fmt.Errorf("invalid workspace archive path %q", value)
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false, fmt.Errorf("invalid workspace archive path %q", value)
		}
	}
	return value, false, nil
}
