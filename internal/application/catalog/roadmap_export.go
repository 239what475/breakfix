package catalog

import (
	"fmt"
	"io"
	"path"
	"strings"

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

	entries, err := challenge.List(challengesDir)
	if err != nil {
		return nil, fmt.Errorf("list materialized challenges: %w", err)
	}
	entriesByID := make(map[string]challenge.Entry, len(entries))
	for _, entry := range entries {
		entriesByID[entry.ID] = entry
	}
	exportedChallenges := make(map[string]exportedChallenge, len(revision.ChallengeBindings))
	for _, binding := range revision.ChallengeBindings {
		entry, exists := entriesByID[binding.Challenge.ID]
		if !exists {
			return nil, fmt.Errorf("roadmap challenge %q is not materialized", binding.Challenge.SourceRef)
		}
		if entry.Title != binding.Challenge.Title || entry.ContentRevision != binding.Challenge.ContentRevision {
			return nil, fmt.Errorf("materialized challenge %q does not match roadmap revision", binding.Challenge.SourceRef)
		}
		value, err := exportChallengeSource(binding, entry)
		if err != nil {
			return nil, err
		}
		exportedChallenges[binding.Challenge.ID] = value
	}

	portable, err := portableRoadmapForExport(revision, exportedChallenges)
	if err != nil {
		return nil, err
	}
	roadmapFiles, err := portableRoadmapFiles(portable)
	if err != nil {
		return nil, err
	}
	manifest := catalogdomain.SourceManifest{
		APIVersion: catalogdomain.SourceAPIVersion,
		Kind:       catalogdomain.SourceKind,
		Metadata:   catalogdomain.SourceMetadata{Name: roadmapExportName, Version: roadmapExportVersion},
		Entries:    make([]catalogdomain.SourceEntry, 0, len(revision.ChallengeBindings)),
		Roadmap:    catalogdomain.SourceRoadmap{ContentRevision: contentRevisionForFiles(roadmapFiles)},
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
	files = append(files, prefixedSourceFiles(roadmapSourcesDirname, roadmapFiles)...)
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

func portableRoadmapForExport(revision roadmapdomain.Revision, challenges map[string]exportedChallenge) (roadmapdomain.PortableRevision, error) {
	result := roadmapdomain.PortableRevision{
		Domains:           make([]roadmapdomain.PortableDomain, 0, len(revision.Domains)),
		Topics:            make([]roadmapdomain.PortableTopic, 0, len(revision.Topics)),
		Tags:              make([]roadmapdomain.PortableTag, 0, len(revision.Tags)),
		ChallengeBindings: make([]roadmapdomain.PortableChallengeBinding, 0, len(revision.ChallengeBindings)),
		TopicEdges:        make([]roadmapdomain.PortableEdge, 0, len(revision.TopicEdges)),
		ChallengeEdges:    make([]roadmapdomain.PortableEdge, 0, len(revision.ChallengeEdges)),
	}
	for _, value := range revision.Domains {
		result.Domains = append(result.Domains, roadmapdomain.PortableDomain{
			Kind: roadmapdomain.KindDomain, SourceRef: value.SourceRef, Title: value.Title, Definition: value.Definition,
			Scope: value.Scope, NonGoals: value.NonGoals,
		})
	}
	for _, value := range revision.Topics {
		result.Topics = append(result.Topics, roadmapdomain.PortableTopic{
			Kind: roadmapdomain.KindTopic, SourceRef: value.SourceRef, Title: value.Title, Domain: portableRef(value.Domain),
			Definition: value.Definition, Scope: value.Scope, NonGoals: value.NonGoals, ChallengeGuidance: value.ChallengeGuidance,
		})
	}
	for _, value := range revision.Tags {
		result.Tags = append(result.Tags, roadmapdomain.PortableTag{
			Kind: roadmapdomain.KindTag, SourceRef: value.SourceRef, Title: value.Title, Description: value.Description,
		})
	}
	for _, value := range revision.ChallengeBindings {
		exported, exists := challenges[value.Challenge.ID]
		if !exists {
			return roadmapdomain.PortableRevision{}, fmt.Errorf("missing exported challenge %q", value.Challenge.SourceRef)
		}
		binding := roadmapdomain.PortableChallengeBinding{
			Kind: roadmapdomain.KindChallenge,
			Challenge: roadmapdomain.PortableChallengeRef{
				Path: exported.path, SourceRef: value.Challenge.SourceRef, Title: value.Challenge.Title,
				ContentRevision: string(exported.contentRevision),
			},
			Topic: portableRef(value.Topic),
			Tags:  make([]roadmapdomain.PortableRef, 0, len(value.Tags)),
		}
		for _, tag := range value.Tags {
			binding.Tags = append(binding.Tags, portableRef(tag))
		}
		result.ChallengeBindings = append(result.ChallengeBindings, binding)
	}
	for _, value := range revision.TopicEdges {
		result.TopicEdges = append(result.TopicEdges, portableEdge(value))
	}
	for _, value := range revision.ChallengeEdges {
		result.ChallengeEdges = append(result.ChallengeEdges, portableEdge(value))
	}
	if err := result.Validate(); err != nil {
		return roadmapdomain.PortableRevision{}, fmt.Errorf("validate exported portable roadmap: %w", err)
	}
	return result, nil
}

func portableRef(value roadmapdomain.Ref) roadmapdomain.PortableRef {
	return roadmapdomain.PortableRef{SourceRef: value.SourceRef, Title: value.Title}
}

func portableEdge(value roadmapdomain.Edge) roadmapdomain.PortableEdge {
	return roadmapdomain.PortableEdge{
		Source: portableRef(value.Source), Target: portableRef(value.Target), Relation: value.Relation, Reason: value.Reason,
	}
}

func portableRoadmapFiles(revision roadmapdomain.PortableRevision) ([]sourceFile, error) {
	files := make([]sourceFile, 0, len(revision.Domains)+len(revision.Topics)+len(revision.Tags)+len(revision.ChallengeBindings)+2)
	for _, value := range revision.Domains {
		file, err := marshalSourceFile(path.Join("domains", value.SourceRef+".yaml"), value)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	for _, value := range revision.Topics {
		file, err := marshalSourceFile(path.Join("topics", value.SourceRef+".yaml"), value)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	for _, value := range revision.Tags {
		file, err := marshalSourceFile(path.Join("tags", value.SourceRef+".yaml"), value)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	for _, value := range revision.ChallengeBindings {
		filename := strings.ReplaceAll(value.Challenge.SourceRef, "/", "--") + ".yaml"
		file, err := marshalSourceFile(path.Join("challenge-bindings", filename), value)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	topicEdges, err := marshalSourceFile("topic-edges.yaml", revision.TopicEdges)
	if err != nil {
		return nil, err
	}
	challengeEdges, err := marshalSourceFile("challenge-edges.yaml", revision.ChallengeEdges)
	if err != nil {
		return nil, err
	}
	files = append(files, topicEdges, challengeEdges)
	return sortedSourceFiles(files), nil
}

func marshalSourceFile(filename string, value any) (sourceFile, error) {
	content, err := yaml.Marshal(value)
	if err != nil {
		return sourceFile{}, fmt.Errorf("marshal exported roadmap %s: %w", filename, err)
	}
	return sourceFile{Path: filename, Content: content}, nil
}

func prefixedSourceFiles(prefix string, files []sourceFile) []sourceFile {
	result := make([]sourceFile, 0, len(files))
	for _, file := range files {
		result = append(result, sourceFile{Path: path.Join(prefix, file.Path), Content: file.Content, Executable: file.Executable})
	}
	return result
}
