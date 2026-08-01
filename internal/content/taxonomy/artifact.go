package taxonomy

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	domain "github.com/breakfix/breakfix/internal/domain/taxonomy"
)

// ReadChallengeArtifact produces deterministic, untrusted model context from
// a published challenge directory. It deliberately exposes content only, not
// a host path or any write capability.
func ReadChallengeArtifact(root string) (domain.ChallengeArtifact, error) {
	files := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("taxonomy artifact has unsupported file %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return domain.ChallengeArtifact{}, err
	}
	slices.Sort(files)
	artifact := domain.ChallengeArtifact{Files: make([]domain.ChallengeArtifactFile, 0, len(files))}
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return domain.ChallengeArtifact{}, err
		}
		artifact.Files = append(artifact.Files, domain.ChallengeArtifactFile{Path: rel, Content: string(data)})
	}
	return artifact, nil
}
