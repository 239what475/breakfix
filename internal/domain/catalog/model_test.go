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

func TestBundleDigestUsesOnlyImmutableDigestSyntax(t *testing.T) {
	for _, value := range []BundleDigest{"", "sha256:ABC", "registry.example/catalog@" + testRevision, BundleDigest(testRevision)} {
		want := value == BundleDigest(testRevision)
		if value.Valid() != want {
			t.Fatalf("BundleDigest(%q).Valid() = %v, want %v", value, value.Valid(), want)
		}
	}
}
