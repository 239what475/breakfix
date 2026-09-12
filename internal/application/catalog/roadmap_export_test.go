package catalog

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	roadmap "github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestRoadmapRevisionExportIsPortableAndByteStable(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	writeChallengeSource(t, candidate, false)
	contentRevision, err := ContentRevision(candidate)
	if err != nil {
		t.Fatal(err)
	}
	challengesDir := filepath.Join(root, "materialized")
	published, err := challenge.PromoteDirectoryAt(
		challengesDir, candidate, "chal-export-one", "chrev-aaaaaaaaaaaaaaaa", "cleanup-logs-one", strings.Repeat("a", 64), string(contentRevision), time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("materialize challenge: %v", err)
	}
	publishedSecond, err := challenge.PromoteDirectoryAt(
		challengesDir, candidate, "chal-export-two", "chrev-bbbbbbbbbbbbbbbb", "cleanup-logs-two", strings.Repeat("b", 64), string(contentRevision), time.Date(2026, time.August, 4, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("materialize second challenge: %v", err)
	}
	domain := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindDomain, "linux"), SourceRef: "linux", Title: "Linux operations"}
	topic := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "linux/shell-files"), SourceRef: "linux/shell-files", Title: "Shell and files"}
	secondTopic := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "linux/services"), SourceRef: "linux/services", Title: "Services"}
	tag := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTag, "shell"), SourceRef: "shell", Title: "Shell"}
	revision := roadmap.Revision{
		Revision: catalogTestRevision('f'),
		Domains:  []roadmap.Domain{{ID: domain.ID, SourceRef: domain.SourceRef, Title: domain.Title, Definition: "Operate and recover Linux systems.", Scope: "Files and shell tooling.", NonGoals: "Kernel development."}},
		Topics: []roadmap.Topic{
			{ID: topic.ID, SourceRef: topic.SourceRef, Title: topic.Title, Domain: domain, Definition: "Recover shell and file state.", Scope: "Shell execution and files.", NonGoals: "Service orchestration.", ChallengeGuidance: "Use for shell and file recovery."},
			{ID: secondTopic.ID, SourceRef: secondTopic.SourceRef, Title: secondTopic.Title, Domain: domain, Definition: "Operate services.", Scope: "Service state.", NonGoals: "Shell semantics.", ChallengeGuidance: "Use for service recovery."},
		},
		Tags: []roadmap.Tag{{ID: tag.ID, SourceRef: tag.SourceRef, Title: tag.Title, Description: "The task materially depends on shell behavior."}},
		ChallengeBindings: []roadmap.ChallengeBinding{
			{Challenge: roadmap.ChallengeRef{ID: published.ID, RevisionID: published.RevisionID, SourceRef: "linux/shell-files/cleanup-logs", Title: published.Title, ContentRevision: published.ContentRevision, SourceSlug: published.SourceSlug, MaterializedRevision: published.Revision}, Topic: topic, Tags: []roadmap.Ref{tag}},
			{Challenge: roadmap.ChallengeRef{ID: publishedSecond.ID, RevisionID: publishedSecond.RevisionID, SourceRef: "linux/services/cleanup-logs", Title: publishedSecond.Title, ContentRevision: publishedSecond.ContentRevision, SourceSlug: publishedSecond.SourceSlug, MaterializedRevision: publishedSecond.Revision}, Topic: secondTopic},
		},
		TopicEdges: []roadmap.Edge{{Source: topic, Target: secondTopic, Relation: roadmap.RelationPrecedes, Reason: "Shell state provides useful operational context."}},
		ChallengeEdges: []roadmap.Edge{{
			Source: roadmap.Ref{ID: published.ID, SourceRef: "linux/shell-files/cleanup-logs", Title: published.Title},
			Target: roadmap.Ref{ID: publishedSecond.ID, SourceRef: "linux/services/cleanup-logs", Title: publishedSecond.Title}, Relation: roadmap.RelationPrecedes, Reason: "Repair shell state first.",
		}},
	}

	export, err := PrepareRoadmapRevisionExport(revision, challengesDir)
	if err != nil {
		t.Fatalf("prepare export: %v", err)
	}
	var first bytes.Buffer
	if err := export.WriteArchiveTo(&first); err != nil {
		t.Fatalf("write first export: %v", err)
	}
	var second bytes.Buffer
	if err := export.WriteArchiveTo(&second); err != nil {
		t.Fatalf("write second export: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("repeated revision export is not byte stable")
	}

	extracted := filepath.Join(root, "extracted")
	if err := os.MkdirAll(extracted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := challenge.ExtractTarGz(extracted, bytes.NewReader(first.Bytes())); err != nil {
		t.Fatalf("extract export: %v", err)
	}
	source, err := LoadPortableSource(extracted)
	if err != nil {
		t.Fatalf("load exported portable release: %v", err)
	}
	if source.Manifest.Metadata.Name != roadmapExportName || source.Manifest.Metadata.Version != roadmapExportVersion || len(source.Challenges) != 2 {
		t.Fatalf("exported release manifest = %#v", source.Manifest)
	}
	if source.Challenges[0].Entry.ID != "" || source.Challenges[0].Entry.Image != "" || !source.Challenges[0].Entry.PublishedAt.IsZero() {
		t.Fatalf("export retained published challenge data: %#v", source.Challenges[0].Entry)
	}
	if _, err := os.Stat(filepath.Join(extracted, "roadmap")); !os.IsNotExist(err) {
		t.Fatalf("portable export retained roadmap sources: %v", err)
	}
	if info, err := os.Stat(filepath.Join(extracted, "challenges", "linux", "shell-files", "cleanup-logs", "nodes", "host", "generate.sh")); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("export did not retain executable source mode: %v, %#o", err, info.Mode().Perm())
	}
}
