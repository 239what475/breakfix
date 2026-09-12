package catalog

import (
	"fmt"
	"io"
	"path"

	"github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
	"gopkg.in/yaml.v3"
)

const (
	roadmapExportName    = "breakfix-roadmap-export"
	roadmapExportVersion = "v1"
)

// RoadmapRevisionExport is a complete portable Catalog Release prepared from
// one immutable RoadmapRevision. It only retains source content, so it can be
// written repeatedly without any temporary directory or runtime artifact.
type RoadmapRevisionExport struct {
	filename string
	files    []sourceFile
}

func (e *RoadmapRevisionExport) Filename() string {
	if e == nil {
		return ""
	}
	return e.filename
}

// WriteArchiveTo streams the deterministic portable release tar.gz to destination.
func (e *RoadmapRevisionExport) WriteArchiveTo(destination io.Writer) error {
	if e == nil || len(e.files) == 0 {
		return fmt.Errorf("roadmap export is empty")
	}
	return writeSourceArchiveFiles(destination, e.files)
}

// PrepareRoadmapRevisionExport reads the materialized source required by one
// immutable revision and turns it into a portable Catalog Release. Published
// challenge metadata is removed before the release is assembled.
func PrepareRoadmapRevisionExport(revision roadmapdomain.Revision, challengesDir string) (*RoadmapRevisionExport, error) {
	if !roadmapdomain.ValidRevision(revision.Revision) {
		return nil, fmt.Errorf("invalid roadmap revision %q", revision.Revision)
	}
	revision = revision.Sorted()
	if err := revision.Validate(); err != nil {
		return nil, fmt.Errorf("validate roadmap revision: %w", err)
	}
	if len(revision.ChallengeBindings) == 0 {
		return nil, fmt.Errorf("roadmap revision %q has no challenges to export", revision.Revision)
	}

	entries, err := materializedChallengeIndex(revision, challengesDir)
	if err != nil {
		return nil, fmt.Errorf("validate materialized challenges for export: %w", err)
	}
	exportedChallenges := make(map[string]exportedChallenge, len(revision.ChallengeBindings))
	for _, binding := range revision.ChallengeBindings {
		entry, exists := entries[binding.Challenge.ID]
		if !exists {
			return nil, fmt.Errorf("roadmap challenge %q is not materialized", binding.Challenge.SourceRef)
		}
		value, err := exportChallengeSource(binding, entry)
		if err != nil {
			return nil, err
		}
		exportedChallenges[binding.Challenge.ID] = value
	}

	manifest := catalogdomain.SourceManifest{
		APIVersion: catalogdomain.SourceAPIVersion,
		Kind:       catalogdomain.SourceKind,
		Metadata:   catalogdomain.SourceMetadata{Name: roadmapExportName, Version: roadmapExportVersion},
		Entries:    make([]catalogdomain.SourceEntry, 0, len(revision.ChallengeBindings)),
	}
	for _, binding := range revision.ChallengeBindings {
		value := exportedChallenges[binding.Challenge.ID]
		manifest.Entries = append(manifest.Entries, catalogdomain.SourceEntry{Path: value.path, ContentRevision: value.contentRevision})
	}
	manifestBytes, err := yaml.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal exported release manifest: %w", err)
	}

	files := []sourceFile{{Path: releaseManifestFilename, Content: manifestBytes}}
	for _, binding := range revision.ChallengeBindings {
		value := exportedChallenges[binding.Challenge.ID]
		files = append(files, prefixedSourceFiles(value.path, value.files)...)
	}
	return &RoadmapRevisionExport{
		filename: "breakfix-roadmap-r" + revision.Revision + ".tar.gz",
		files:    sortedSourceFiles(files),
	}, nil
}

type exportedChallenge struct {
	path            string
	files           []sourceFile
	contentRevision catalogdomain.ContentRevision
}

func exportChallengeSource(binding roadmapdomain.ChallengeBinding, entry challenge.Entry) (exportedChallenge, error) {
	files, err := readSourceFiles(entry.Dir)
	if err != nil {
		return exportedChallenge{}, fmt.Errorf("read materialized challenge %q: %w", binding.Challenge.SourceRef, err)
	}
	foundManifest := false
	for index := range files {
		if files[index].Path != "challenge.yaml" {
			continue
		}
		portable, err := removePublishedFields(files[index].Content)
		if err != nil {
			return exportedChallenge{}, fmt.Errorf("make challenge %q portable: %w", binding.Challenge.SourceRef, err)
		}
		files[index].Content = portable
		foundManifest = true
		break
	}
	if !foundManifest {
		return exportedChallenge{}, fmt.Errorf("materialized challenge %q has no manifest", binding.Challenge.SourceRef)
	}
	return exportedChallenge{
		path:            path.Join(challengeSourcesDirname, binding.Challenge.SourceRef),
		files:           files,
		contentRevision: contentRevisionForFiles(files),
	}, nil
}

func prefixedSourceFiles(prefix string, files []sourceFile) []sourceFile {
	result := make([]sourceFile, 0, len(files))
	for _, file := range files {
		result = append(result, sourceFile{Path: path.Join(prefix, file.Path), Content: file.Content, Executable: file.Executable})
	}
	return result
}
