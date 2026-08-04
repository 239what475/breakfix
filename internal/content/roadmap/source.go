// Package roadmap reads a human-maintained portable Roadmap tree. It only
// parses and validates source; publishing immutable runtime revisions belongs
// to the database repository.
package roadmap

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	domain "github.com/breakfix/breakfix/internal/domain/roadmap"
	"gopkg.in/yaml.v3"
)

const (
	domainsDir           = "domains"
	topicsDir            = "topics"
	tagsDir              = "tags"
	challengeBindingsDir = "challenge-bindings"
	topicEdgesFile       = "topic-edges.yaml"
	challengeEdgesFile   = "challenge-edges.yaml"
)

// LoadPortable reads a complete Roadmap source tree. The source layout is
// intentionally part of the portable Catalog Release contract, so unknown
// files, symlinks, and multiple YAML documents are rejected.
func LoadPortable(root string) (domain.PortableRevision, error) {
	root, err := validateRoot(root)
	if err != nil {
		return domain.PortableRevision{}, err
	}
	result := domain.PortableRevision{}
	if result.Domains, err = readFiles[domain.PortableDomain](filepath.Join(root, domainsDir), func(value *domain.PortableDomain, file string) { value.File = file }); err != nil {
		return domain.PortableRevision{}, fmt.Errorf("read domains: %w", err)
	}
	if result.Topics, err = readFilesRecursive[domain.PortableTopic](filepath.Join(root, topicsDir), func(value *domain.PortableTopic, file string) { value.File = file }); err != nil {
		return domain.PortableRevision{}, fmt.Errorf("read topics: %w", err)
	}
	if result.Tags, err = readFiles[domain.PortableTag](filepath.Join(root, tagsDir), func(value *domain.PortableTag, file string) { value.File = file }); err != nil {
		return domain.PortableRevision{}, fmt.Errorf("read tags: %w", err)
	}
	if result.ChallengeBindings, err = readFiles[domain.PortableChallengeBinding](filepath.Join(root, challengeBindingsDir), func(value *domain.PortableChallengeBinding, file string) { value.File = file }); err != nil {
		return domain.PortableRevision{}, fmt.Errorf("read challenge bindings: %w", err)
	}
	if result.TopicEdges, err = readList[domain.PortableEdge](filepath.Join(root, topicEdgesFile)); err != nil {
		return domain.PortableRevision{}, fmt.Errorf("read topic edges: %w", err)
	}
	if result.ChallengeEdges, err = readList[domain.PortableEdge](filepath.Join(root, challengeEdgesFile)); err != nil {
		return domain.PortableRevision{}, fmt.Errorf("read challenge edges: %w", err)
	}
	if err := result.Validate(); err != nil {
		return domain.PortableRevision{}, fmt.Errorf("validate portable roadmap: %w", err)
	}
	return result, nil
}

func validateRoot(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("stat portable roadmap: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("portable roadmap must be a directory, not a symlink")
	}
	for _, relative := range []string{domainsDir, topicsDir, tagsDir, challengeBindingsDir} {
		child, err := os.Lstat(filepath.Join(root, relative))
		if err != nil {
			return "", fmt.Errorf("read portable roadmap directory %s: %w", relative, err)
		}
		if child.Mode()&os.ModeSymlink != 0 || !child.IsDir() {
			return "", fmt.Errorf("portable roadmap path %s must be a directory", relative)
		}
	}
	for _, filename := range []string{topicEdgesFile, challengeEdgesFile} {
		child, err := os.Lstat(filepath.Join(root, filename))
		if err != nil {
			return "", fmt.Errorf("read portable roadmap file %s: %w", filename, err)
		}
		if child.Mode()&os.ModeSymlink != 0 || !child.Mode().IsRegular() {
			return "", fmt.Errorf("portable roadmap file %s must be regular", filename)
		}
	}
	if err := validateTree(root); err != nil {
		return "", err
	}
	return filepath.Clean(root), nil
}

func validateTree(root string) error {
	allowed := map[string]bool{
		".": true, domainsDir: true, topicsDir: true, tagsDir: true, challengeBindingsDir: true,
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("portable roadmap does not allow symlink %s", relative)
		}
		if entry.IsDir() {
			if relative == topicsDir || strings.HasPrefix(relative, topicsDir+"/") {
				return nil
			}
			if !allowed[relative] {
				return fmt.Errorf("unexpected portable roadmap directory %s", relative)
			}
			return nil
		}
		if relative == topicEdgesFile || relative == challengeEdgesFile {
			return nil
		}
		parent := filepath.ToSlash(filepath.Dir(relative))
		validParent := parent == domainsDir || parent == tagsDir || parent == challengeBindingsDir || parent == topicsDir || strings.HasPrefix(parent, topicsDir+"/")
		if !validParent || !strings.HasSuffix(entry.Name(), ".yaml") || !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected portable roadmap file %s", relative)
		}
		return nil
	})
}

func readFiles[T any](directory string, setFile func(*T, string)) ([]T, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		paths = append(paths, entry.Name())
	}
	slices.Sort(paths)
	result := make([]T, 0, len(paths))
	for _, name := range paths {
		value, err := readYAML[T](filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		setFile(&value, strings.TrimSuffix(name, ".yaml"))
		result = append(result, value)
	}
	return result, nil
}

func readFilesRecursive[T any](root string, setFile func(*T, string)) ([]T, error) {
	paths := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".yaml") {
			return fmt.Errorf("topic source only allows YAML files, got %s", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		return nil, err
	}
	slices.Sort(paths)
	result := make([]T, 0, len(paths))
	for _, relative := range paths {
		value, err := readYAML[T](filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", relative, err)
		}
		setFile(&value, strings.TrimSuffix(relative, ".yaml"))
		result = append(result, value)
	}
	return result, nil
}

func readList[T any](filename string) ([]T, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var result []T
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not allowed")
		}
		return nil, err
	}
	return result, nil
}

func readYAML[T any](filename string) (T, error) {
	var value T
	data, err := os.ReadFile(filename)
	if err != nil {
		return value, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, errors.New("multiple YAML documents are not allowed")
		}
		return value, err
	}
	return value, nil
}
