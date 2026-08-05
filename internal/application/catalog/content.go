package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	contentrevision "github.com/breakfix/breakfix/internal/content/revision"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
)

// ContentRevision returns the canonical content identity for one portable
// source tree. Relative paths are byte-sorted and every regular file
// contributes its raw bytes and executable bit. YAML is never normalized.
func ContentRevision(root string) (catalogdomain.ContentRevision, error) {
	revision, err := contentrevision.Directory(root)
	if err != nil {
		return "", err
	}
	return catalogdomain.ContentRevision(revision), nil
}

func contentRevisionForFiles(files []sourceFile) catalogdomain.ContentRevision {
	canonical := make([]contentrevision.File, 0, len(files))
	for _, file := range files {
		canonical = append(canonical, contentrevision.File{Path: file.Path, Content: file.Content, Executable: file.Executable})
	}
	return catalogdomain.ContentRevision(contentrevision.Files(canonical))
}

type sourceFile struct {
	Path       string
	Content    []byte
	Executable bool
}

func readSourceFiles(root string) ([]sourceFile, error) {
	canonical, err := contentrevision.ReadDirectory(root)
	if err != nil {
		return nil, err
	}
	files := make([]sourceFile, 0, len(canonical))
	for _, file := range canonical {
		files = append(files, sourceFile{Path: file.Path, Content: file.Content, Executable: file.Executable})
	}
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
