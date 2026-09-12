package generation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/content/workspacearchive"
)

// Candidate is the immutable archive inspected by the Generator and Judge
// before it can enter the real candidate pipeline. The Worker only holds it
// in memory; Server is the authority that persists a passed candidate.
type Candidate struct {
	Archive []byte
	Entry   scenario.Entry
	Files   []CandidateFile
}

// CandidateFile is deliberately limited to regular workspace files. It is
// used as untrusted data in the Judge prompt, never as instruction text.
type CandidateFile struct {
	Path    string
	Content string
}

// InspectCandidateArchive validates the canonical archive returned by the
// workspace. Platform fields are still rejected by scenario validation; the
// archive format itself has no provider-owned metadata semantics.
func InspectCandidateArchive(archive []byte) (*Candidate, error) {
	if len(archive) == 0 {
		return nil, errors.New("generator candidate archive is empty")
	}
	canonical, err := workspacearchive.Canonicalize(archive)
	if err != nil {
		return nil, fmt.Errorf("validate generator candidate archive: %w", err)
	}
	dir, err := os.MkdirTemp("", "breakfix-generator-candidate-")
	if err != nil {
		return nil, fmt.Errorf("create candidate staging: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	if err := workspacearchive.Extract(dir, canonical); err != nil {
		return nil, fmt.Errorf("extract generator candidate: %w", err)
	}
	entry, err := ValidateCandidateDir(dir)
	if err != nil {
		return nil, err
	}
	files, err := candidateFiles(dir)
	if err != nil {
		return nil, err
	}
	return &Candidate{Archive: canonical, Entry: *entry, Files: files}, nil
}

// ValidateCandidateDir validates a generator-owned scenario directory. In
// contrast with a published catalog entry, it must not contain platform-owned
// id, source slug, image, or publication metadata. They are added only by
// publication.
func ValidateCandidateDir(chalDir string) (*scenario.Entry, error) {
	entry, err := scenario.ValidatePortableDir(chalDir)
	if err != nil {
		return nil, err
	}
	if err := scenario.RequireOperationsScenario(entry); err != nil {
		return nil, err
	}
	return entry, nil
}
func candidateFiles(root string) ([]CandidateFile, error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open candidate root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()
	files := make([]CandidateFile, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("candidate contains unsupported file %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := rootFS.ReadFile(rel)
		if err != nil {
			return err
		}
		files = append(files, CandidateFile{Path: filepath.ToSlash(rel), Content: string(content)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read candidate files: %w", err)
	}
	return files, nil
}
