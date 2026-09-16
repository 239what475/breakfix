package docsproject

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFindOrphansExcludesTreeRoot(t *testing.T) {
	root := t.TempDir()
	dirs := []string{"docs", "docs/home", "docs/reference/generated/v1"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(dir), "index.html"), []byte("<html></html>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	orphans, err := findOrphans(root, map[string]struct{}{"docs/home/": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0] != "docs/reference/generated/v1/" {
		t.Fatalf("orphans = %#v, want only docs/reference/generated/v1/", orphans)
	}
}

func TestRunRejectsInvalidInvocationWithInputExitCode(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name   string
		config Config
	}{
		{name: "version", config: Config{Root: root, Out: "out", Workers: 1}},
		{name: "root", config: Config{Out: "out", Version: "v1", Workers: 1}},
		{name: "output", config: Config{Root: root, Version: "v1", Workers: 1}},
		{name: "workers", config: Config{Root: root, Out: "out", Version: "v1"}},
		{name: "missing root", config: Config{Root: root + "/missing", Out: "out", Version: "v1", Workers: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Run(test.config)
			var input *InputError
			if !errors.As(err, &input) || ExitCode(err) != 2 {
				t.Fatalf("Run() error = %v, exit code = %d; want input error and 2", err, ExitCode(err))
			}
		})
	}
}

func TestRunAcceptsDocumentedMinimumConfig(t *testing.T) {
	config := DefaultConfig()
	config.Root = sidebarRoot(t, sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/")))
	config.Out = t.TempDir()
	config.Version = "docs-project-v1"
	writeNormalizerFile(t, config.Root, "build-info.json", `{"source":"kubernetes","revision":"abc123","version":"snapshot","locale":"en","base_url":"http://localhost:1313/"}`)
	writeNormalizerFile(t, config.Root, "_redirects", "")
	if err := Run(config); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestLoadTreeStripsLocaleControlsAndFindsOrphans(t *testing.T) {
	root := sidebarRoot(t, sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Getting started", "/docs/setup/", sidebar("Install", "/docs/setup/install/"))))
	writeTreePage(t, root, "docs/setup/")
	writeTreePage(t, root, "docs/setup/install/")
	writeTreePage(t, root, "docs/reference/orphan/")
	writeTreePage(t, root, "docs/reference/orphan/_print/")

	state, err := loadTree(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := state.Pages, []string{"docs/home/", "docs/setup/", "docs/setup/install/"}; !sameStrings(got, want) {
		t.Fatalf("pages = %#v, want %#v", got, want)
	}
	if got, want := state.Orphans, []string{"docs/reference/orphan/"}; !sameStrings(got, want) {
		t.Fatalf("orphans = %#v, want %#v", got, want)
	}
	if len(state.Tree.Nodes) != 2 || state.Tree.Nodes[0].Path != "docs/home/" {
		t.Fatalf("tree = %#v", state.Tree)
	}
}

func TestLoadTreeRejectsChangedSidebarAndMissingMapping(t *testing.T) {
	root := sidebarRoot(t, sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Setup", "/docs/setup/")))
	if _, err := loadTree(root, nil); err == nil {
		t.Fatal("missing tree mapping was accepted")
	}
	writeTreePage(t, root, "docs/setup/")
	writeTreePageContents(t, root, "docs/setup/", sidebarDocument(sidebar("Documentation", "/docs/", sidebar("Other", "/docs/other/"))))
	if _, err := loadTree(root, nil); err == nil {
		t.Fatal("inconsistent sidebar was accepted")
	}
}

func TestLoadTreeSubsetSkipsCrossPageValidation(t *testing.T) {
	root := sidebarRoot(t, sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Setup", "/docs/setup/")))
	writeTreePage(t, root, "docs/setup/")
	writeTreePageContents(t, root, "docs/setup/", sidebarDocument(sidebar("Documentation", "/docs/", sidebar("Other", "/docs/other/"))))
	state, err := loadTree(root, []string{"/docs/setup/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Warnings) != 2 || state.Warnings[0] != "cross-page sidebar validation skipped for -pages subset" || len(state.Pages) != 1 || state.Pages[0] != "docs/setup/" {
		t.Fatalf("subset state = %#v", state)
	}
}

func sidebarRoot(t *testing.T, navigation string) string {
	t.Helper()
	root := t.TempDir()
	writeTreePageContents(t, root, "docs/home/", sidebarDocument(navigation))
	return root
}

func sidebarDocument(navigation string) string {
	return "<html><body><nav id=\"td-section-nav\"><div><a href=\"/fr/docs/\">French</a></div><ul>" + navigation + "</ul></nav><main><h1>Page</h1></main></body></html>"
}

func sidebar(title, path string, children ...string) string {
	childList := ""
	if len(children) > 0 {
		childList = "<ul>" + join(children) + "</ul>"
	}
	return "<li><label><a href=\"" + path + "\">" + title + "</a></label>" + childList + "</li>"
}

func join(values []string) string {
	result := ""
	for _, value := range values {
		result += value
	}
	return result
}

func writeTreePage(t *testing.T, root, path string) {
	t.Helper()
	writeTreePageContents(t, root, path, sidebarDocument(sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Getting started", "/docs/setup/", sidebar("Install", "/docs/setup/install/")))))
}

func writeTreePageContents(t *testing.T, root, path, content string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(path), "index.html")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestExitCodeUsesOneForPageFailures(t *testing.T) {
	if got := ExitCode(errors.New("page failure")); got != 1 {
		t.Fatalf("ExitCode() = %d, want 1", got)
	}
}
