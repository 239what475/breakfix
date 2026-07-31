package catalogseed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdatePublishedImagePreservesManifestFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "challenge.yaml")
	const oldImage = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const newImage = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const manifest = "id: chal-example\nsource_slug: example\nruntime: node\nimage: " + oldImage + "\ndescription: |\n  Keep this indentation.\n"
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := updatePublishedImage(dir, newImage); err != nil {
		t.Fatalf("update published image: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(manifest, oldImage, newImage, 1)
	if string(got) != want {
		t.Fatalf("manifest = %q, want %q", got, want)
	}
}
