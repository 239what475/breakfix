package docsproject

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type referencedAsset struct {
	Asset
	content []byte
}

// copyReferencedAssets assembles the library's self-contained asset set after
// page extraction has produced the authoritative per-page asset manifests.
func copyReferencedAssets(config Config, manifests []PageManifest) (int, []PageFailure) {
	assets, failures := collectReferencedAssets(manifests)
	if len(failures) > 0 {
		return 0, failures
	}
	for index := range assets {
		content, err := readReferencedAsset(config.Root, assets[index].Path)
		if err != nil {
			failures = append(failures, PageFailure{Path: assets[index].Path, Error: err.Error()})
			continue
		}
		if actual := digest(content); actual != assets[index].Digest {
			failures = append(failures, PageFailure{Path: assets[index].Path, Error: fmt.Sprintf("source asset digest = %s, want %s", actual, assets[index].Digest)})
			continue
		}
		assets[index].content = content
	}
	if len(failures) > 0 {
		return 0, failures
	}

	for _, asset := range assets {
		target, err := assetFile(config.Out, asset.Path)
		if err != nil {
			failures = append(failures, PageFailure{Path: asset.Path, Error: err.Error()})
			continue
		}
		info, err := os.Lstat(target)
		if err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			failures = append(failures, PageFailure{Path: asset.Path, Error: "library asset is not a regular file"})
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, PageFailure{Path: asset.Path, Error: fmt.Sprintf("stat library asset: %v", err)})
			continue
		}
		if !config.Resume || errors.Is(err, os.ErrNotExist) {
			continue
		}
		content, err := os.ReadFile(target)
		if err != nil {
			failures = append(failures, PageFailure{Path: asset.Path, Error: fmt.Sprintf("read library asset: %v", err)})
			continue
		}
		if actual := digest(content); actual != asset.Digest {
			failures = append(failures, PageFailure{Path: asset.Path, Error: fmt.Sprintf("library asset digest = %s, want %s", actual, asset.Digest)})
		}
	}
	if len(failures) > 0 {
		return 0, failures
	}

	for _, asset := range assets {
		target, _ := assetFile(config.Out, asset.Path)
		if config.Resume {
			content, err := os.ReadFile(target)
			if err == nil && digest(content) == asset.Digest {
				continue
			}
		}
		if err := writeAtomically(target, asset.content); err != nil {
			return 0, []PageFailure{{Path: asset.Path, Error: fmt.Sprintf("write library asset: %v", err)}}
		}
	}
	return len(assets), nil
}

func collectReferencedAssets(manifests []PageManifest) ([]referencedAsset, []PageFailure) {
	byPath := make(map[string]referencedAsset)
	var failures []PageFailure
	for _, manifest := range manifests {
		for _, asset := range manifest.Assets {
			if _, err := assetFile(".", asset.Path); err != nil {
				failures = append(failures, PageFailure{Path: asset.Path, Error: err.Error()})
				continue
			}
			existing, found := byPath[asset.Path]
			if found && existing.Digest != asset.Digest {
				failures = append(failures, PageFailure{Path: asset.Path, Error: "page manifests record conflicting asset digests"})
				continue
			}
			byPath[asset.Path] = referencedAsset{Asset: asset}
		}
	}
	assets := make([]referencedAsset, 0, len(byPath))
	for _, asset := range byPath {
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(left, right int) bool { return assets[left].Path < assets[right].Path })
	sort.Slice(failures, func(left, right int) bool { return failures[left].Path < failures[right].Path })
	return assets, failures
}

func readReferencedAsset(root, assetPath string) ([]byte, error) {
	filename, err := assetFile(root, assetPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source asset is missing")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read source asset: %w", err)
	}
	return content, nil
}

func assetFile(root, assetPath string) (string, error) {
	if strings.TrimSpace(assetPath) == "" || path.IsAbs(assetPath) {
		return "", errors.New("asset path is not a relative site path")
	}
	cleaned := path.Clean(assetPath)
	if cleaned == "." || cleaned != assetPath || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("asset path escapes the library root")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	filename, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(cleaned)))
	if err != nil || !strings.HasPrefix(filename, root+string(filepath.Separator)) {
		return "", errors.New("asset path escapes the library root")
	}
	return filename, nil
}
