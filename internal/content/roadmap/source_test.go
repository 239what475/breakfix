package roadmap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPortableRetainsSourcePaths(t *testing.T) {
	root := t.TempDir()
	writeRoadmapSourceFile(t, filepath.Join(root, "domains", "linux.yaml"), "kind: Domain\nsource_ref: linux\ntitle: Linux\ndefinition: Operate Linux systems.\nscope: Linux operations.\nnon_goals: Kernel development.\n")
	writeRoadmapSourceFile(t, filepath.Join(root, "topics", "linux", "files.yaml"), "kind: Topic\nsource_ref: linux/files\ntitle: Files\ndomain:\n  source_ref: linux\n  title: Linux\ndefinition: Repair file state.\nscope: Files.\nnon_goals: Kernel development.\nchallenge_guidance: Use for file recovery.\n")
	writeRoadmapSourceFile(t, filepath.Join(root, "tags", "shell.yaml"), "kind: Tag\nsource_ref: shell\ntitle: Shell\ndescription: Shell operations.\n")
	writeRoadmapSourceFile(t, filepath.Join(root, "challenge-bindings", "cleanup.yaml"), "kind: Challenge\nchallenge:\n  path: challenges/linux/files/cleanup\n  source_ref: linux/files/cleanup\n  title: Cleanup\n  content_revision: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\ntopic:\n  source_ref: linux/files\n  title: Files\ntags:\n  - source_ref: shell\n    title: Shell\n")
	writeRoadmapSourceFile(t, filepath.Join(root, "topic-edges.yaml"), "[]\n")
	writeRoadmapSourceFile(t, filepath.Join(root, "challenge-edges.yaml"), "[]\n")
	loaded, err := LoadPortable(root)
	if err != nil {
		t.Fatalf("load portable roadmap: %v", err)
	}
	if len(loaded.Domains) != 1 || len(loaded.Topics) != 1 || len(loaded.Tags) != 1 || len(loaded.ChallengeBindings) != 1 {
		t.Fatalf("loaded roadmap is incomplete: %#v", loaded)
	}
	if loaded.Topics[0].File == "" || loaded.ChallengeBindings[0].File == "" {
		t.Fatalf("source paths were not retained: %#v %#v", loaded.Topics, loaded.ChallengeBindings)
	}
}

func writeRoadmapSourceFile(t *testing.T, filename, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
