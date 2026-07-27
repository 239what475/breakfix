package authoring

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/pmezard/go-difflib/difflib"
)

const maxAssetBytes = 512 * 1024

type Asset struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type FileDiff struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

func ArtifactDirectory(dataDir, sessionID string, revision int64) string {
	return filepath.Join(dataDir, "authoring", sessionID, "revisions", strconv.FormatInt(revision, 10), "artifact")
}

func ArtifactRelativePath(sessionID string, revision int64) string {
	return filepath.ToSlash(filepath.Join("authoring", sessionID, "revisions", strconv.FormatInt(revision, 10), "artifact"))
}

func ReadAssets(dataDir string, artifact *Artifact) ([]Asset, error) {
	if artifact == nil || strings.TrimSpace(artifact.Directory) == "" {
		return []Asset{}, nil
	}
	root, err := ArtifactPath(dataDir, artifact)
	if err != nil {
		return nil, err
	}
	assets := make([]Asset, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("authoring artifact cannot be a symlink: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("authoring artifact is not a regular file: %s", path)
		}
		if info.Size() > maxAssetBytes {
			return fmt.Errorf("authoring artifact exceeds %d bytes: %s", maxAssetBytes, path)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		assets = append(assets, Asset{Path: filepath.ToSlash(rel), Content: string(content)})
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read authoring artifact: %w", err)
	}
	slices.SortFunc(assets, func(left, right Asset) int { return strings.Compare(left.Path, right.Path) })
	return assets, nil
}

// ArtifactPath resolves an artifact reference stored by the authoring
// repository and rejects paths outside data/authoring.
func ArtifactPath(dataDir string, artifact *Artifact) (string, error) {
	if artifact == nil || strings.TrimSpace(artifact.Directory) == "" {
		return "", errors.New("authoring artifact directory is empty")
	}
	return artifactPath(dataDir, artifact.Directory)
}

// ReadVerifiedChallenge returns the metadata and public checkpoints from the
// exact manifest that passed VerifyTask. The authoring intent remains separate
// so a generator correction cannot be accidentally hidden by stale planning
// metadata.
func ReadVerifiedChallenge(dataDir string, artifact *Artifact) (*VerifiedChallenge, error) {
	root, err := ArtifactPath(dataDir, artifact)
	if err != nil {
		return nil, err
	}
	entry, err := challenge.ValidateSubmissionDir(root)
	if err != nil {
		return nil, fmt.Errorf("read verified challenge manifest: %w", err)
	}
	result := &VerifiedChallenge{
		Metadata: Metadata{
			Title:       entry.Title,
			Difficulty:  entry.Difficulty,
			Description: entry.Description,
			Runtime:     entry.Runtime,
		},
		Checkpoints: make([]VerifiedCheckpoint, 0, len(entry.Checkpoints)),
	}
	for _, checkpoint := range entry.Checkpoints {
		result.Checkpoints = append(result.Checkpoints, VerifiedCheckpoint{
			ID:          checkpoint.ID,
			Title:       checkpoint.Title,
			Description: checkpoint.Description,
			Hint:        checkpoint.Hint,
			DependsOn:   append([]string{}, checkpoint.DependsOn...),
		})
	}
	return result, nil
}

func DiffAssets(dataDir string, current, previous *Artifact) ([]FileDiff, error) {
	currentAssets, err := ReadAssets(dataDir, current)
	if err != nil {
		return nil, err
	}
	previousAssets, err := ReadAssets(dataDir, previous)
	if err != nil {
		return nil, err
	}
	currentByPath := make(map[string]string, len(currentAssets))
	previousByPath := make(map[string]string, len(previousAssets))
	for _, asset := range currentAssets {
		currentByPath[asset.Path] = asset.Content
	}
	for _, asset := range previousAssets {
		previousByPath[asset.Path] = asset.Content
	}
	paths := make([]string, 0, len(currentByPath)+len(previousByPath))
	for path := range currentByPath {
		paths = append(paths, path)
	}
	for path := range previousByPath {
		if _, ok := currentByPath[path]; !ok {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	diffs := make([]FileDiff, 0, len(paths))
	for _, path := range paths {
		before := previousByPath[path]
		after := currentByPath[path]
		if before == after {
			continue
		}
		diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(before),
			B:        difflib.SplitLines(after),
			FromFile: "previous/" + path,
			ToFile:   "current/" + path,
			Context:  3,
		})
		if err != nil {
			return nil, fmt.Errorf("diff authoring artifact %s: %w", path, err)
		}
		diffs = append(diffs, FileDiff{Path: path, Diff: diff})
	}
	return diffs, nil
}

func artifactPath(dataDir, relative string) (string, error) {
	base := filepath.Clean(filepath.Join(dataDir, "authoring"))
	path := filepath.Clean(filepath.Join(dataDir, filepath.FromSlash(relative)))
	prefix := base + string(os.PathSeparator)
	if path != base && !strings.HasPrefix(path, prefix) {
		return "", fmt.Errorf("invalid authoring artifact path")
	}
	return path, nil
}
