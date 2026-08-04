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

	"github.com/breakfix/breakfix/internal/content/challenge"
	roadmapsource "github.com/breakfix/breakfix/internal/content/roadmap"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
	"gopkg.in/yaml.v3"
)

const (
	releaseManifestFilename = "release.yaml"
	challengeSourcesDirname = "challenges"
	roadmapSourcesDirname   = "roadmap"
)

// PortableSource is a validated catalog source tree. It contains source
// identity only; platform challenge IDs and runtime artifacts are created by
// the installer after real verification succeeds.
type PortableSource struct {
	Root       string
	Manifest   catalogdomain.SourceManifest
	Challenges []SourceChallenge
	Roadmap    roadmapdomain.PortableRevision
}

type SourceChallenge struct {
	Path            string
	Entry           challenge.Entry
	ContentRevision catalogdomain.ContentRevision
}

// LoadPortableSource validates the complete checked-in catalog source. It
// does not install, build, or make any challenge visible.
func LoadPortableSource(root string) (*PortableSource, error) {
	root, err := validateSourceRoot(root)
	if err != nil {
		return nil, err
	}
	manifest, err := readReleaseManifest(filepath.Join(root, releaseManifestFilename))
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("validate release manifest: %w", err)
	}
	if err := validateDistinctChallengeRoots(manifest.Entries); err != nil {
		return nil, err
	}
	if err := validateChallengeSourceLayout(root, manifest.Entries); err != nil {
		return nil, err
	}

	result := &PortableSource{Root: root, Manifest: manifest, Challenges: make([]SourceChallenge, 0, len(manifest.Entries))}
	for _, declared := range manifest.Entries {
		dir, err := sourcePath(root, declared.Path)
		if err != nil {
			return nil, err
		}
		revision, err := ContentRevision(dir)
		if err != nil {
			return nil, fmt.Errorf("hash release challenge %q: %w", declared.Path, err)
		}
		if revision != declared.ContentRevision {
			return nil, fmt.Errorf("release challenge %q contentRevision is %q, want %q", declared.Path, revision, declared.ContentRevision)
		}
		entry, err := challenge.ValidatePortableDir(dir)
		if err != nil {
			return nil, fmt.Errorf("validate release challenge %q: %w", declared.Path, err)
		}
		result.Challenges = append(result.Challenges, SourceChallenge{
			Path: declared.Path, Entry: *entry, ContentRevision: revision,
		})
	}

	roadmapRoot := filepath.Join(root, roadmapSourcesDirname)
	roadmapRevision, snapshot, err := loadPortableRoadmap(roadmapRoot)
	if err != nil {
		return nil, err
	}
	if roadmapRevision != manifest.Roadmap.ContentRevision {
		return nil, fmt.Errorf("release roadmap contentRevision is %q, want %q", roadmapRevision, manifest.Roadmap.ContentRevision)
	}
	if err := validateRoadmapSources(snapshot, result.Challenges); err != nil {
		return nil, err
	}
	result.Roadmap = snapshot
	return result, nil
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
	expected := map[string]bool{releaseManifestFilename: false, challengeSourcesDirname: false, roadmapSourcesDirname: false}
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

func loadPortableRoadmap(root string) (catalogdomain.ContentRevision, roadmapdomain.PortableRevision, error) {
	revision, err := ContentRevision(root)
	if err != nil {
		return "", roadmapdomain.PortableRevision{}, fmt.Errorf("hash portable roadmap: %w", err)
	}
	snapshot, err := roadmapsource.LoadPortable(root)
	if err != nil {
		return "", roadmapdomain.PortableRevision{}, err
	}
	return revision, snapshot, nil
}

func validateRoadmapSources(snapshot roadmapdomain.PortableRevision, challenges []SourceChallenge) error {
	expected := make(map[string]SourceChallenge, len(challenges))
	for _, source := range challenges {
		expected[source.Path] = source
	}
	if len(snapshot.ChallengeBindings) != len(expected) {
		return fmt.Errorf("portable roadmap has %d challenge bindings, want %d", len(snapshot.ChallengeBindings), len(expected))
	}
	for _, binding := range snapshot.ChallengeBindings {
		source, exists := expected[binding.Challenge.Path]
		if !exists {
			return fmt.Errorf("portable roadmap binding references unknown challenge source %q", binding.Challenge.Path)
		}
		if binding.Challenge.Title != source.Entry.Title {
			return fmt.Errorf("portable roadmap binding title for %q does not match source", binding.Challenge.Path)
		}
		if binding.Challenge.ContentRevision != string(source.ContentRevision) {
			return fmt.Errorf("portable roadmap binding contentRevision for %q does not match source", binding.Challenge.Path)
		}
	}
	return nil
}

func validateDistinctChallengeRoots(entries []catalogdomain.SourceEntry) error {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.Path)
	}
	slices.Sort(paths)
	for index := 1; index < len(paths); index++ {
		if strings.HasPrefix(paths[index], paths[index-1]+"/") {
			return fmt.Errorf("release challenge paths %q and %q overlap", paths[index-1], paths[index])
		}
	}
	return nil
}

func validateChallengeSourceLayout(root string, entries []catalogdomain.SourceEntry) error {
	entryPaths := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryPaths = append(entryPaths, entry.Path)
	}
	challengesRoot := filepath.Join(root, challengeSourcesDirname)
	return filepath.WalkDir(challengesRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == challengesRoot {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("catalog challenge source does not allow symlink %s", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("catalog challenge source does not allow non-regular file %s", rel)
		}
		if info.IsDir() {
			for _, expected := range entryPaths {
				if rel == expected || strings.HasPrefix(expected, rel+"/") || strings.HasPrefix(rel, expected+"/") {
					return nil
				}
			}
			return fmt.Errorf("catalog challenge source has unreferenced directory %s", rel)
		}
		for _, expected := range entryPaths {
			if strings.HasPrefix(rel, expected+"/") {
				return nil
			}
		}
		return fmt.Errorf("catalog challenge source has unreferenced file %s", rel)
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
