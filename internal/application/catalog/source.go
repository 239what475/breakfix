package catalog

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"gopkg.in/yaml.v3"
)

const (
	releaseManifestFilename = "release.yaml"
	scenarioSourcesDirname  = "scenarios"
)

// PortableSource is a validated catalog source tree. It contains source
// identity only; platform scenario IDs and runtime artifacts are created by
// the installer after real verification succeeds.
type PortableSource struct {
	Root      string
	Manifest  catalogdomain.SourceManifest
	Scenarios []SourceScenario
}

type SourceScenario struct {
	Path            string
	Entry           scenario.Entry
	ContentRevision catalogdomain.ContentRevision
}

// ContentRevisionReport contains the revisions that must be copied into a
// Catalog Release manifest after editing its source tree. It intentionally
// does not use the manifest's declared values, so it can repair stale ones.
type ContentRevisionReport struct {
	Entries []ContentRevisionEntry `json:"entries"`
}

type ContentRevisionEntry struct {
	Path            string                        `json:"path"`
	ContentRevision catalogdomain.ContentRevision `json:"contentRevision"`
}

// LoadPortableSource validates the complete checked-in catalog source. It
// does not install, build, or make any scenario visible.
func LoadPortableSource(root string) (*PortableSource, error) {
	result, revisions, err := loadPortableSource(root)
	if err != nil {
		return nil, err
	}
	for index, declared := range result.Manifest.Entries {
		actual := revisions.Entries[index].ContentRevision
		if actual != declared.ContentRevision {
			return nil, fmt.Errorf("release scenario %q contentRevision is %q, want %q", declared.Path, actual, declared.ContentRevision)
		}
	}
	return result, nil
}

// CalculateContentRevisions validates the source tree and computes its
// current revisions without trusting the values declared in release.yaml.
// This is the repair path used before packaging a changed release source.
func CalculateContentRevisions(root string) (ContentRevisionReport, error) {
	_, revisions, err := loadPortableSource(root)
	return revisions, err
}

func loadPortableSource(root string) (*PortableSource, ContentRevisionReport, error) {
	root, err := validateSourceRoot(root)
	if err != nil {
		return nil, ContentRevisionReport{}, err
	}
	manifest, err := readReleaseManifest(filepath.Join(root, releaseManifestFilename))
	if err != nil {
		return nil, ContentRevisionReport{}, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, ContentRevisionReport{}, fmt.Errorf("validate release manifest: %w", err)
	}
	if err := validateDistinctScenarioRoots(manifest.Entries); err != nil {
		return nil, ContentRevisionReport{}, err
	}
	if err := validateScenarioSourceLayout(root, manifest.Entries); err != nil {
		return nil, ContentRevisionReport{}, err
	}

	result := &PortableSource{Root: root, Manifest: manifest, Scenarios: make([]SourceScenario, 0, len(manifest.Entries))}
	revisions := ContentRevisionReport{Entries: make([]ContentRevisionEntry, 0, len(manifest.Entries))}
	for _, declared := range manifest.Entries {
		dir, err := sourcePath(root, declared.Path)
		if err != nil {
			return nil, ContentRevisionReport{}, err
		}
		revision, err := ContentRevision(dir)
		if err != nil {
			return nil, ContentRevisionReport{}, fmt.Errorf("hash release scenario %q: %w", declared.Path, err)
		}
		entry, err := scenario.ValidatePortableDir(dir)
		if err != nil {
			return nil, ContentRevisionReport{}, fmt.Errorf("validate release scenario %q: %w", declared.Path, err)
		}
		result.Scenarios = append(result.Scenarios, SourceScenario{
			Path: declared.Path, Entry: *entry, ContentRevision: revision,
		})
		revisions.Entries = append(revisions.Entries, ContentRevisionEntry{Path: declared.Path, ContentRevision: revision})
	}

	return result, revisions, nil
}

func readReleaseManifest(filename string) (catalogdomain.SourceManifest, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return catalogdomain.SourceManifest{}, fmt.Errorf("read catalog release manifest: %w", err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	var manifest catalogdomain.SourceManifest
	if err := decoder.Decode(&manifest); err != nil {
		return catalogdomain.SourceManifest{}, fmt.Errorf("parse catalog release manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return catalogdomain.SourceManifest{}, errors.New("catalog release manifest must contain one YAML document")
		}
		return catalogdomain.SourceManifest{}, fmt.Errorf("parse catalog release manifest: %w", err)
	}
	return manifest, nil
}

func validateSourceRoot(root string) (string, error) {
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("stat catalog source root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("catalog source root %q must be a directory, not a symlink", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read catalog source root: %w", err)
	}
	expected := map[string]bool{releaseManifestFilename: false, scenarioSourcesDirname: false}
	for _, entry := range entries {
		_, exists := expected[entry.Name()]
		if !exists {
			return "", fmt.Errorf("unexpected catalog source entry %q", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("catalog source entry %q must not be a symlink", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if entry.Name() == releaseManifestFilename {
			if !info.Mode().IsRegular() {
				return "", errors.New("catalog release.yaml must be a regular file")
			}
		} else if !info.IsDir() {
			return "", fmt.Errorf("catalog source entry %q must be a directory", entry.Name())
		}
		expected[entry.Name()] = true
	}
	for name, present := range expected {
		if !present {
			return "", fmt.Errorf("catalog source is missing %s", name)
		}
	}
	return root, nil
}

func validateDistinctScenarioRoots(entries []catalogdomain.SourceEntry) error {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	slices.Sort(paths)
	for index := 1; index < len(paths); index++ {
		if strings.HasPrefix(paths[index], paths[index-1]+"/") {
			return fmt.Errorf("release scenario paths %q and %q overlap", paths[index-1], paths[index])
		}
	}
	return nil
}

func validateScenarioSourceLayout(root string, entries []catalogdomain.SourceEntry) error {
	entryPaths := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryPaths = append(entryPaths, entry.Path)
	}
	scenariosRoot := filepath.Join(root, scenarioSourcesDirname)
	return filepath.WalkDir(scenariosRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == scenariosRoot {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("catalog scenario source does not allow symlink %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("catalog scenario source does not allow non-regular file %s", rel)
		}
		if info.IsDir() {
			for _, expected := range entryPaths {
				if rel == expected || strings.HasPrefix(expected, rel+"/") || strings.HasPrefix(rel, expected+"/") {
					return nil
				}
			}
			return fmt.Errorf("catalog scenario source has unreferenced directory %s", rel)
		}
		for _, expected := range entryPaths {
			if strings.HasPrefix(rel, expected+"/") {
				return nil
			}
		}
		return fmt.Errorf("catalog scenario source has unreferenced file %s", rel)
	})
}

func sourcePath(root, relative string) (string, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if cleanPath == cleanRoot || !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("catalog source path %q escapes root", relative)
	}
	return cleanPath, nil
}
