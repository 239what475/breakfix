package roadmap

import (
	"path/filepath"
	"testing"
)

func TestLoadPortableReadsCatalogReleaseRoadmapSource(t *testing.T) {
	root := filepath.Join("..", "..", "..", "test", "fixtures", "catalog-release", "roadmap")
	loaded, err := LoadPortable(root)
	if err != nil {
		t.Fatalf("load portable roadmap: %v", err)
	}
	if len(loaded.Domains) != 3 || len(loaded.Topics) != 3 || len(loaded.Tags) != 1 || len(loaded.ChallengeBindings) != 1 {
		t.Fatalf("loaded roadmap is incomplete: %#v", loaded)
	}
	if loaded.Topics[0].File == "" || loaded.ChallengeBindings[0].File == "" {
		t.Fatalf("source paths were not retained: %#v %#v", loaded.Topics, loaded.ChallengeBindings)
	}
}
