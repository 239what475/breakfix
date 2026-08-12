package mcpconnector

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
)

func TestReviewProjectorAtomicallyProjectsAndResynchronizesContent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "reviews")
	projector, err := NewReviewProjector(root)
	if err != nil {
		t.Fatalf("new review projector: %v", err)
	}
	bundle := testReviewBundle(t, "content", "NeedsAuthorReview", 0, map[string]string{
		"overview.md":                    "# Candidate\n",
		"checkpoints/ready.md":           "# Ready\n",
		"candidate/problem.md":           "# Problem\n",
		"candidate/nodes/host/checks.sh": "#!/bin/sh\n",
		"diff/problem.md.diff":           "--- old\n+++ new\n",
		"judge.md":                       "# Judge\n",
		"verification.md":                "# Verification\n",
	})
	first, err := projector.Project(bundle)
	if err != nil {
		t.Fatalf("project content review: %v", err)
	}
	want := filepath.Join(root, "generation-workflow-one", "content", "candidate-revision-one")
	if first.ReviewPath != want || first.Kind != "content" || first.ProposalRevision != 0 {
		t.Fatalf("projection = %#v, want path %q", first, want)
	}
	assertReviewProjection(t, first.ReviewPath, bundle.Manifest, "local-token", "sandbox_id", "pvc_name", "10.0.0.1")

	second, err := projector.Project(bundle)
	if err != nil {
		t.Fatalf("repeat project: %v", err)
	}
	if second != first {
		t.Fatalf("repeat projection = %#v, want %#v", second, first)
	}
	if err := os.RemoveAll(first.ReviewPath); err != nil {
		t.Fatalf("remove local projection: %v", err)
	}
	third, err := projector.Project(bundle)
	if err != nil {
		t.Fatalf("resynchronize deleted projection: %v", err)
	}
	if third != first {
		t.Fatalf("resynchronized projection = %#v, want %#v", third, first)
	}
	assertReviewProjection(t, third.ReviewPath, bundle.Manifest, "local-token", "sandbox_id", "pvc_name", "10.0.0.1")
}

func TestReviewProjectorRejectsIncorrectPayloadDigest(t *testing.T) {
	projector, err := NewReviewProjector(filepath.Join(t.TempDir(), "reviews"))
	if err != nil {
		t.Fatalf("new review projector: %v", err)
	}
	bundle := testReviewBundle(t, "classification", "NeedsClassificationReview", 3, map[string]string{
		"topic.md": "# Topic\n", "tags.md": "# Tags\n", "classification.md": "# Classification\n",
	})
	bundle.Manifest.PayloadSha256 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := projector.Project(bundle); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("project mismatched payload = %v, want digest failure", err)
	}
}

func assertReviewProjection(t *testing.T, root string, manifest api.GeneratorReviewManifest, forbidden ...string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatalf("read projected manifest: %v", err)
	}
	var projected api.GeneratorReviewManifest
	if err := json.Unmarshal(data, &projected); err != nil {
		t.Fatalf("decode projected manifest: %v", err)
	}
	if !sameReviewManifest(projected, manifest) {
		t.Fatalf("projected manifest = %#v, want %#v", projected, manifest)
	}
	var all strings.Builder
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			t.Fatalf("review projection contains symlink %s", path)
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("review projection directory is not private: %s mode=%o", path, info.Mode().Perm())
			}
			return nil
		}
		if entry.Type().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			all.Write(content)
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm()&0o222 != 0 {
				t.Fatalf("review projection file remains writable: %s mode=%o", path, info.Mode().Perm())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk review projection: %v", err)
	}
	for _, value := range forbidden {
		if strings.Contains(all.String(), value) {
			t.Fatalf("review projection leaked %q", value)
		}
	}
}

func testReviewBundle(t *testing.T, kind, state string, proposalRevision int, entries map[string]string) api.GeneratorReviewBundle {
	t.Helper()
	payload := testReviewPayload(t, entries)
	manifest := api.GeneratorReviewManifest{
		SchemaVersion:          reviewBundleSchemaVersion,
		Kind:                   api.GeneratorReviewManifestKind(kind),
		WorkflowId:             "generation-workflow-one",
		WorkflowState:          state,
		CandidateRevisionId:    "candidate-revision-one",
		CandidateArchiveSha256: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ProposalRevision:       proposalRevision,
		PayloadSha256:          reviewPayloadDigest(payload),
		ExportedAt:             time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC),
	}
	return api.GeneratorReviewBundle{Manifest: manifest, Payload: base64.StdEncoding.EncodeToString(payload)}
}

func testReviewPayload(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, content := range entries {
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write review payload header: %v", err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatalf("write review payload data: %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close review payload tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close review payload gzip: %v", err)
	}
	return data.Bytes()
}
