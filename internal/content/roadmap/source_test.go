package roadmap

import (
	"path/filepath"
	"testing"
)

func TestLoadPortableReadsReadableDomainGroupedSource(t *testing.T) {
	root := filepath.Join("..", "..", "..", "test", "fixtures", "roadmap")
	loaded, err := LoadPortable(root)
	if err != nil {
		t.Fatalf("load portable roadmap: %v", err)
	}
	if len(loaded.Domains) != 1 || len(loaded.Topics) != 2 || len(loaded.Tags) != 1 || len(loaded.ChallengeBindings) != 2 {
		t.Fatalf("loaded roadmap is incomplete: %#v", loaded)
	}
	if loaded.Topics[0].File == "" || loaded.ChallengeBindings[0].File == "" {
		t.Fatalf("source paths were not retained: %#v %#v", loaded.Topics, loaded.ChallengeBindings)
	}
}
