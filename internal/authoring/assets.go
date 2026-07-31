package authoring

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// ReadAssets projects an immutable CandidateRevision archive for author
// review. The archive remains the sole content copy; extraction is temporary.
func ReadAssets(archive []byte) ([]Asset, error) {
	if len(archive) == 0 {
		return []Asset{}, nil
	}
	var assets []Asset
	err := withCandidateArchive(archive, func(root string, _ *challenge.Entry) error {
		rootFS, err := os.OpenRoot(root)
		if err != nil {
			return err
		}
		defer func() { _ = rootFS.Close() }()
		assets = make([]Asset, 0)
		return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("candidate asset cannot be a symlink: %s", path)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("candidate asset is not a regular file: %s", path)
			}
			if info.Size() > maxAssetBytes {
				return fmt.Errorf("candidate asset exceeds %d bytes: %s", maxAssetBytes, path)
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			content, err := rootFS.ReadFile(relative)
			if err != nil {
				return err
			}
			assets = append(assets, Asset{Path: filepath.ToSlash(relative), Content: string(content)})
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("read candidate assets: %w", err)
	}
	slices.SortFunc(assets, func(left, right Asset) int { return strings.Compare(left.Path, right.Path) })
	return assets, nil
}

func ReadVerifiedChallenge(archive []byte) (*VerifiedChallenge, error) {
	if len(archive) == 0 {
		return nil, nil
	}
	var result *VerifiedChallenge
	err := withCandidateArchive(archive, func(_ string, entry *challenge.Entry) error {
		result = &VerifiedChallenge{
			Metadata: Metadata{
				Title: entry.Title, Difficulty: entry.Difficulty, Description: entry.Description, Runtime: entry.Runtime,
			},
			Checkpoints: make([]VerifiedCheckpoint, 0, len(entry.Checkpoints)),
		}
		for _, checkpoint := range entry.Checkpoints {
			result.Checkpoints = append(result.Checkpoints, VerifiedCheckpoint{
				ID: checkpoint.ID, Title: checkpoint.Title, Description: checkpoint.Description,
				Hint: checkpoint.Hint, Node: checkpoint.Node,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read verified challenge manifest: %w", err)
	}
	return result, nil
}

func DiffAssets(currentArchive, previousArchive []byte) ([]FileDiff, error) {
	currentAssets, err := ReadAssets(currentArchive)
	if err != nil {
		return nil, err
	}
	previousAssets, err := ReadAssets(previousArchive)
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
			A: difflib.SplitLines(before), B: difflib.SplitLines(after),
			FromFile: "previous/" + path, ToFile: "current/" + path, Context: 3,
		})
		if err != nil {
			return nil, fmt.Errorf("diff candidate asset %s: %w", path, err)
		}
		diffs = append(diffs, FileDiff{Path: path, Diff: diff})
	}
	return diffs, nil
}

func withCandidateArchive(archive []byte, consume func(string, *challenge.Entry) error) error {
	root, err := os.MkdirTemp("", "breakfix-author-review-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	if err := challenge.ExtractTarGz(root, bytes.NewReader(archive)); err != nil {
		return err
	}
	entry, err := challenge.ValidateCandidateDir(root)
	if err != nil {
		return err
	}
	return consume(root, entry)
}
