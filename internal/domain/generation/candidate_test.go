package generation

import (
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestRevisionRequiresFrozenPublicSource(t *testing.T) {
	revision := Revision{
		ID: "candidate-revision-01", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session-01"}, SourceRevision: "1",
		ArchivePath: "candidates/candidate-revision-01.tar.gz", ArchiveDigest: digest("a"), ContentRevision: digest("b"),
		SourceArchive: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-sources/candidate-revision-01.tar.gz", Digest: digest("c")},
	}
	if err := revision.ValidateForCreate(); err != nil {
		t.Fatalf("validate frozen revision: %v", err)
	}
	revision.SourceArchive.Digest = ""
	if err := revision.ValidateForCreate(); err == nil {
		t.Fatal("candidate without public source archive was accepted")
	}
}

func TestNewRevisionCannotContainLifecycleOutputs(t *testing.T) {
	revision := Revision{
		ID: "candidate-revision-02", Source: Source{Kind: SourceAuthoring, Ref: "authoring-session-02"}, SourceRevision: "1",
		ArchivePath: "candidates/candidate-revision-02.tar.gz", ArchiveDigest: digest("a"), ContentRevision: digest("b"),
		SourceArchive:       runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-sources/candidate-revision-02.tar.gz", Digest: digest("c")},
		RunnableRevisionRef: &runnable.RevisionReference{ID: "runnable-revision-01", Digest: digest("d")},
	}
	if err := revision.ValidateForCreate(); err == nil {
		t.Fatal("new candidate with materialized runnable revision was accepted")
	}
}

func digest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
