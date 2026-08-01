package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	taxonomydomain "github.com/breakfix/breakfix/internal/domain/taxonomy"
	"gopkg.in/yaml.v3"
)

func readReleaseManifest(path string) (catalogdomain.SourceManifest, error) {
	manifest, err := readYAML[catalogdomain.SourceManifest](path)
	if err != nil {
		return catalogdomain.SourceManifest{}, fmt.Errorf("read release manifest: %w", err)
	}
	return manifest, nil
}

func loadPortableTaxonomy(root string) (catalogdomain.ContentRevision, taxonomydomain.PortableSnapshot, error) {
	if err := validatePortableTaxonomyTree(root); err != nil {
		return "", taxonomydomain.PortableSnapshot{}, err
	}
	revision, err := ContentRevision(root)
	if err != nil {
		return "", taxonomydomain.PortableSnapshot{}, fmt.Errorf("hash portable taxonomy: %w", err)
	}
	snapshot := taxonomydomain.PortableSnapshot{}
	if snapshot.Skills, err = readTaxonomyDefinitions[taxonomydomain.Skill](filepath.Join(root, "skills"), func(value *taxonomydomain.Skill, file string) { value.File = file }); err != nil {
		return "", taxonomydomain.PortableSnapshot{}, err
	}
	if snapshot.Tags, err = readTaxonomyDefinitions[taxonomydomain.Tag](filepath.Join(root, "tags"), func(value *taxonomydomain.Tag, file string) { value.File = file }); err != nil {
		return "", taxonomydomain.PortableSnapshot{}, err
	}
	if snapshot.ChallengeMappings, err = readTaxonomyDefinitions[taxonomydomain.PortableChallengeMapping](filepath.Join(root, "mappings", "challenges"), func(value *taxonomydomain.PortableChallengeMapping, file string) { value.File = file }); err != nil {
		return "", taxonomydomain.PortableSnapshot{}, err
	}
	if snapshot.SkillMappings, err = readOptionalTaxonomyDefinitions[taxonomydomain.SkillMapping](filepath.Join(root, "mappings", "skills"), func(value *taxonomydomain.SkillMapping, file string) { value.File = file }); err != nil {
		return "", taxonomydomain.PortableSnapshot{}, err
	}
	if err := taxonomydomain.ValidatePortable(snapshot); err != nil {
		return "", taxonomydomain.PortableSnapshot{}, fmt.Errorf("validate portable taxonomy: %w", err)
	}
	return revision, snapshot, nil
}

func validatePortableTaxonomyTree(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("stat portable taxonomy: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("portable taxonomy must be a directory, not a symlink")
	}
	required := []string{"skills", "tags", filepath.Join("mappings", "challenges")}
	for _, relative := range required {
		info, err := os.Lstat(filepath.Join(root, relative))
		if err != nil {
			return fmt.Errorf("read portable taxonomy directory %s: %w", filepath.ToSlash(relative), err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("portable taxonomy path %s must be a directory", filepath.ToSlash(relative))
		}
	}
	allowedDirectories := map[string]bool{
		".": true, "skills": true, "tags": true, "mappings": true, "mappings/challenges": true, "mappings/skills": true,
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("portable taxonomy does not allow symlink %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			if !allowedDirectories[rel] {
				return fmt.Errorf("unexpected portable taxonomy directory %s", rel)
			}
			return nil
		}
		if !info.Mode().IsRegular() || !strings.HasSuffix(entry.Name(), ".yaml") {
			return fmt.Errorf("portable taxonomy only allows YAML files, got %s", rel)
		}
		parent := filepath.ToSlash(filepath.Dir(rel))
		if parent != "skills" && parent != "tags" && parent != "mappings/challenges" && parent != "mappings/skills" {
			return fmt.Errorf("unexpected portable taxonomy file %s", rel)
		}
		if strings.TrimSuffix(entry.Name(), ".yaml") == "" {
			return fmt.Errorf("invalid portable taxonomy filename %s", rel)
		}
		return nil
	})
}

func validateTaxonomySources(snapshot taxonomydomain.PortableSnapshot, challenges []SourceChallenge) error {
	expected := make(map[string]SourceChallenge, len(challenges))
	for _, source := range challenges {
		expected[source.Path] = source
	}
	if len(snapshot.ChallengeMappings) != len(expected) {
		return fmt.Errorf("portable taxonomy has %d challenge mappings, want %d", len(snapshot.ChallengeMappings), len(expected))
	}
	for _, mapping := range snapshot.ChallengeMappings {
		source, exists := expected[mapping.Challenge.Path]
		if !exists {
			return fmt.Errorf("portable taxonomy mapping references unknown challenge source %q", mapping.Challenge.Path)
		}
		if mapping.Challenge.Title != source.Entry.Title {
			return fmt.Errorf("portable taxonomy mapping title for %q does not match source", mapping.Challenge.Path)
		}
		if mapping.Challenge.ContentRevision != string(source.ContentRevision) {
			return fmt.Errorf("portable taxonomy mapping contentRevision for %q does not match source", mapping.Challenge.Path)
		}
	}
	return nil
}

func readTaxonomyDefinitions[T any](dir string, setFile func(*T, string)) ([]T, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	values := make([]T, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		value, err := readYAML[T](filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read portable taxonomy file %s: %w", entry.Name(), err)
		}
		setFile(&value, strings.TrimSuffix(entry.Name(), ".yaml"))
		values = append(values, value)
	}
	return values, nil
}

func readOptionalTaxonomyDefinitions[T any](dir string, setFile func(*T, string)) ([]T, error) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	return readTaxonomyDefinitions(dir, setFile)
}

func readYAML[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
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
