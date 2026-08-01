package catalog

import "testing"

const testRevision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestContentRevisionRejectsRuntimeAndMalformedReferences(t *testing.T) {
	for _, value := range []ContentRevision{"", "sha256:ABC", "registry.example/challenge@" + testRevision, ContentRevision(testRevision)} {
		want := value == ContentRevision(testRevision)
		if value.Valid() != want {
			t.Fatalf("ContentRevision(%q).Valid() = %v, want %v", value, value.Valid(), want)
		}
	}
}

func TestReleaseAndEntryKeepPortableIdentity(t *testing.T) {
	release := Release{
		ID: "release-1", Name: "foundation", Version: "2026.08.01", BundleDigest: BundleDigest(testRevision),
		TaxonomyContentRevision: ContentRevision(testRevision), State: ReleasePending,
	}
	if !release.Valid() {
		t.Fatalf("valid release rejected: %#v", release)
	}
	entry := Entry{
		ID: "entry-1", ReleaseID: release.ID, SourcePath: "challenges/linux/cleanup-logs",
		ContentRevision: ContentRevision(testRevision), State: EntryPending,
	}
	if !entry.Valid() {
		t.Fatalf("valid entry rejected: %#v", entry)
	}
	entry.SourcePath = "../outside"
	if entry.Valid() {
		t.Fatal("entry accepted a path outside its portable source bundle")
	}
}
