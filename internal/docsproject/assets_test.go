package docsproject

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunCopiesOnlyReferencedAssets(t *testing.T) {
	_, out, config := assetProjectionFixture(t)
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	globalBytes, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	global, err := DecodeGlobalManifest(globalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if global.Stats.Assets != 2 || global.Stats.AssetsCopied != 2 {
		t.Fatalf("asset stats = %#v, want 2 references and 2 copied assets", global.Stats)
	}
	for path, want := range map[string][]byte{
		"docs/images/used.svg": []byte("used asset\n"),
		"images/docs/pod.svg":  []byte("pod asset\n"),
	} {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("copied asset %q = %q, %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "docs", "images", "unreferenced.svg")); !os.IsNotExist(err) {
		t.Fatalf("unreferenced source asset was copied: %v", err)
	}
	if got, want := regularFiles(t, out), []string{
		"docs/home/index.json",
		"docs/home/index.md",
		"docs/images/used.svg",
		"docs/setup/index.json",
		"docs/setup/index.md",
		"images/docs/pod.svg",
		"manifest.json",
	}; !sameStrings(got, want) {
		t.Fatalf("library files = %#v, want %#v", got, want)
	}
}

func TestRunReportsReferencedAssetDigestMismatch(t *testing.T) {
	root, out, config := assetProjectionFixture(t)
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	writeAsset(t, root, "docs/images/used.svg", []byte("mutated source asset\n"))
	config.Resume = true
	err := Run(config)
	if ExitCode(err) != 1 {
		t.Fatalf("exit code = %d, error = %v", ExitCode(err), err)
	}
	content, readErr := os.ReadFile(filepath.Join(out, "report.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var report FailureReport
	if err := json.Unmarshal(content, &report); err != nil || len(report.Failures) != 1 || report.Failures[0].Path != "docs/images/used.svg" || report.Failures[0].Error == "" {
		t.Fatalf("report = %#v, decode error = %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("manifest remains after asset failure: %v", err)
	}
}

func TestRunResumeSkipsAndRestoresReferencedAssets(t *testing.T) {
	_, out, config := assetProjectionFixture(t)
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	assets := map[string][]byte{
		"docs/images/used.svg": []byte("used asset\n"),
		"images/docs/pod.svg":  []byte("pod asset\n"),
	}
	pageFiles := []string{"docs/home/index.md", "docs/home/index.json", "docs/setup/index.md", "docs/setup/index.json"}
	beforePages := make(map[string][]byte, len(pageFiles))
	for _, filename := range pageFiles {
		content, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(filename)))
		if err != nil {
			t.Fatal(err)
		}
		beforePages[filename] = content
	}
	oldTime := time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)
	for path := range assets {
		filename := filepath.Join(out, filepath.FromSlash(path))
		if err := os.Chtimes(filename, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	config.Resume = true
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	for path := range assets {
		info, err := os.Stat(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil || !info.ModTime().Equal(oldTime) {
			t.Fatalf("matching resume rewrote asset %q: %v, %v", path, info, err)
		}
	}
	for path := range assets {
		if err := os.Remove(filepath.Join(out, filepath.FromSlash(path))); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	for path, want := range assets {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored asset %q = %q, %v", path, got, err)
		}
	}
	for filename, want := range beforePages {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(filename)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("resume changed page output %q = %q, %v", filename, got, err)
		}
	}
}

func assetProjectionFixture(t *testing.T) (string, string, Config) {
	t.Helper()
	root := t.TempDir()
	navigation := sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Setup", "/docs/setup/"))
	writeProjectionPage(t, root, "docs/home/", navigation, "<h1>Home</h1><img src=\"/images/docs/pod.svg\">")
	writeProjectionPage(t, root, "docs/setup/", navigation, "<h1>Setup</h1><img src=\"/docs/images/used.svg\">")
	writeNormalizerFile(t, root, "build-info.json", `{"source":"kubernetes","revision":"abc123","version":"snapshot","locale":"en","base_url":"http://localhost:1313/"}`)
	writeNormalizerFile(t, root, "_redirects", "")
	writeAsset(t, root, "docs/images/used.svg", []byte("used asset\n"))
	writeAsset(t, root, "docs/images/unreferenced.svg", []byte("unreferenced asset\n"))
	writeAsset(t, root, "images/docs/pod.svg", []byte("pod asset\n"))
	out := filepath.Join(t.TempDir(), "documents")
	return root, out, Config{Root: root, Out: out, Workers: 2, Version: "docs-project-v9"}
}

func writeAsset(t *testing.T, root, path string, content []byte) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, content, 0o644); err != nil {
		t.Fatal(err)
	}
}
