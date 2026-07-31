package generator

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/challenge"
	"gopkg.in/yaml.v3"
)

// Candidate is the immutable archive inspected by the Generator and Judge
// before it can enter the real candidate pipeline. The Worker only holds it
// in memory; Server is the authority that persists a passed candidate.
type Candidate struct {
	Archive []byte
	Entry   challenge.Entry
	Files   []CandidateFile
}

// CandidateFile is deliberately limited to regular workspace files. It is
// used as untrusted data in the Judge prompt, never as instruction text.
type CandidateFile struct {
	Path    string
	Content string
}

// InspectCandidateArchive validates exactly the archive returned by the
// workspace. It makes no metadata substitutions or normalizations: platform
// fields are rejected so a model protocol mistake cannot silently change the
// candidate that reaches the build stage.
func InspectCandidateArchive(archive []byte) (*Candidate, error) {
	if len(archive) == 0 {
		return nil, errors.New("generator candidate archive is empty")
	}
	dir, err := os.MkdirTemp("", "breakfix-generator-candidate-")
	if err != nil {
		return nil, fmt.Errorf("create candidate staging: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck
	if err := challenge.ExtractTarGz(dir, bytes.NewReader(archive)); err != nil {
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
	return &Candidate{Archive: append([]byte(nil), archive...), Entry: *entry, Files: files}, nil
}

// ValidateCandidateDir validates a generator-owned challenge directory. In
// contrast with a published catalog entry, it must not contain platform-owned
// id, source slug, image, or publication metadata. They are added only by
// publication.
func ValidateCandidateDir(chalDir string) (*challenge.Entry, error) {
	path := filepath.Join(chalDir, "challenge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read challenge.yaml: %w", err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse challenge.yaml: %w", err)
	}
	if spec == nil {
		return nil, errors.New("challenge.yaml must be a mapping")
	}

	var errs []string
	for _, field := range []string{"id", "source_slug", "image", "published_at"} {
		if _, exists := spec[field]; exists {
			errs = append(errs, fmt.Sprintf("challenge.yaml 不得包含平台托管字段 %q", field))
		}
	}
	runtime := scalarString(spec["runtime"])
	if runtime != challenge.RuntimeNode && runtime != challenge.RuntimeK8s {
		errs = append(errs, fmt.Sprintf("challenge.yaml runtime 必须明确为 node 或 k8s，当前为 %q", runtime))
	}
	if scalarString(spec["title"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 title")
	}
	switch scalarString(spec["difficulty"]) {
	case "easy", "medium", "hard":
	default:
		errs = append(errs, fmt.Sprintf("challenge.yaml difficulty 必须为 easy/medium/hard，当前为 %q", scalarString(spec["difficulty"])))
	}
	if _, exists := spec["tags"]; exists {
		errs = append(errs, "challenge.yaml 不得包含 tags；分类由 taxonomy workflow 维护")
	}
	if scalarString(spec["description"]) == "" {
		errs = append(errs, "challenge.yaml 缺少 description")
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, "; "))
	}
	entry, err := challenge.ValidateCandidateDir(chalDir)
	if err != nil {
		return nil, fmt.Errorf("validate challenge structure: %w", err)
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

func scalarString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
