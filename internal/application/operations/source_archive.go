package operations

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	contentrevision "github.com/breakfix/breakfix/internal/content/revision"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// BuildSourceArchive freezes a validated Operations source tree into the
// public runnable tar.gz format. It derives bytes only from regular files,
// their relative paths, and executable bits; filesystem ordering and timestamps
// cannot change the resulting archive or source digest.
func BuildSourceArchive(root string) (runnable.SourceArchive, []byte, error) {
	files, err := contentrevision.ReadDirectory(root)
	if err != nil {
		return runnable.SourceArchive{}, nil, fmt.Errorf("read operations source tree: %w", err)
	}
	if len(files) == 0 {
		return runnable.SourceArchive{}, nil, fmt.Errorf("operations source tree is empty")
	}

	var data bytes.Buffer
	writer, err := gzip.NewWriterLevel(&data, gzip.BestCompression)
	if err != nil {
		return runnable.SourceArchive{}, nil, fmt.Errorf("create operations source archive: %w", err)
	}
	writer.ModTime = time.Unix(0, 0).UTC()
	writer.OS = 255
	tarWriter := tar.NewWriter(writer)
	for _, file := range files {
		mode := int64(0o644)
		if file.Executable {
			mode = 0o755
		}
		header := &tar.Header{
			Name: file.Path, Mode: mode, Size: int64(len(file.Content)), Uid: 0, Gid: 0,
			ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{}, Format: tar.FormatPAX,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			_ = writer.Close()
			return runnable.SourceArchive{}, nil, fmt.Errorf("write operations source header %q: %w", file.Path, err)
		}
		if _, err := tarWriter.Write(file.Content); err != nil {
			_ = tarWriter.Close()
			_ = writer.Close()
			return runnable.SourceArchive{}, nil, fmt.Errorf("write operations source file %q: %w", file.Path, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = writer.Close()
		return runnable.SourceArchive{}, nil, fmt.Errorf("close operations source tar: %w", err)
	}
	if err := writer.Close(); err != nil {
		return runnable.SourceArchive{}, nil, fmt.Errorf("close operations source archive: %w", err)
	}
	archive := data.Bytes()
	if len(archive) > runnable.MaxSourceArchiveBytes {
		return runnable.SourceArchive{}, nil, fmt.Errorf("operations source archive exceeds the platform limit")
	}
	sum := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	return runnable.SourceArchive{
		FormatVersion: runnable.FormatVersion,
		Reference:     "runnable-source://sha256/" + hex.EncodeToString(sum[:]),
		Digest:        digest,
	}, append([]byte(nil), archive...), nil
}
