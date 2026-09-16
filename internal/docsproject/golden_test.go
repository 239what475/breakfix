package docsproject

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

var fixturePages = []string{
	"docs/home/",
	"docs/concepts/workloads/pods/pod-lifecycle/",
	"docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/",
	"docs/concepts/services-networking/ingress/",
	"docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/",
}

func TestRealPageFixtureMatchesGoldenAcrossRunsAndWorkers(t *testing.T) {
	root := docsProjectFixture(t)
	golden := filepath.Join(root, "golden")
	for _, workers := range []int{1, 8} {
		out := filepath.Join(t.TempDir(), "documents")
		if err := Run(Config{Root: root, Out: out, Workers: workers, Version: "docs-project-v6", Pages: fixturePages}); err != nil {
			t.Fatalf("workers %d: %v", workers, err)
		}
		assertDirectoriesEqual(t, golden, out)
	}
	repeat := filepath.Join(t.TempDir(), "documents")
	if err := Run(Config{Root: root, Out: repeat, Workers: 8, Version: "docs-project-v6", Pages: fixturePages}); err != nil {
		t.Fatal(err)
	}
	assertDirectoriesEqual(t, golden, repeat)
}

func TestRealPageFixtureResumeRebuildsOnlyMissingPairs(t *testing.T) {
	root := docsProjectFixture(t)
	golden := filepath.Join(root, "golden")
	out := filepath.Join(t.TempDir(), "documents")
	config := Config{Root: root, Out: out, Workers: 8, Version: "docs-project-v6", Pages: fixturePages}
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	for index, page := range fixturePages {
		if index%2 == 0 {
			if err := os.Remove(filepath.Join(out, filepath.FromSlash(page), "index.md")); err != nil {
				t.Fatal(err)
			}
		}
	}
	config.Resume = true
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	assertDirectoriesEqual(t, golden, out)
}

func docsProjectFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "test", "fixtures", "docs-project"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "golden", "manifest.json")); err != nil {
		t.Fatalf("docs-project fixture is incomplete: %v", err)
	}
	return root
}

func assertDirectoriesEqual(t *testing.T, expected, actual string) {
	t.Helper()
	if expectedFiles, actualFiles := regularFiles(t, expected), regularFiles(t, actual); !sameStrings(expectedFiles, actualFiles) {
		t.Fatalf("files differ: expected %#v, actual %#v", expectedFiles, actualFiles)
	}
	for _, name := range regularFiles(t, expected) {
		want, err := os.ReadFile(filepath.Join(expected, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(actual, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("file %s differs", name)
		}
	}
}

func regularFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(filename string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}
