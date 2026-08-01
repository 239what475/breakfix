package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/breakfix/breakfix/internal/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"gopkg.in/yaml.v3"
)

// ExportPublishedCandidate copies a published challenge into a portable
// candidate directory. It only removes top-level platform fields from the raw
// manifest and leaves all remaining YAML bytes untouched.
func ExportPublishedCandidate(source, destination string) (catalogdomain.ContentRevision, error) {
	source = filepath.Clean(source)
	destination = filepath.Clean(destination)
	if source == destination {
		return "", errors.New("published challenge source and destination must differ")
	}
	if _, err := challenge.ValidateDir(source); err != nil {
		return "", fmt.Errorf("validate published challenge: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("portable candidate destination %q already exists", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect portable candidate destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", fmt.Errorf("create portable candidate parent: %w", err)
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".tmp-")
	if err != nil {
		return "", fmt.Errorf("create portable candidate staging: %w", err)
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := challenge.CopyRegularFiles(source, staging); err != nil {
		return "", fmt.Errorf("copy published challenge: %w", err)
	}
	manifestPath := filepath.Join(staging, "challenge.yaml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("read staged challenge manifest: %w", err)
	}
	portable, err := removePublishedFields(manifest)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(manifestPath, portable, 0o644); err != nil {
		return "", fmt.Errorf("write portable challenge manifest: %w", err)
	}
	if _, err := challenge.ValidatePortableDir(staging); err != nil {
		return "", fmt.Errorf("validate exported portable challenge: %w", err)
	}
	revision, err := ContentRevision(staging)
	if err != nil {
		return "", fmt.Errorf("hash exported portable challenge: %w", err)
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", fmt.Errorf("promote portable candidate: %w", err)
	}
	promoted = true
	return revision, nil
}

func removePublishedFields(data []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse published challenge manifest: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("published challenge manifest must be a mapping")
	}
	mapping := document.Content[0]
	lineOffsets := yamlLineOffsets(data)
	type span struct{ start, end int }
	spans := make([]span, 0, 4)
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if !isPublishedField(key.Value) {
			continue
		}
		start, err := yamlLineOffset(lineOffsets, key.Line)
		if err != nil {
			return nil, err
		}
		end := len(data)
		if index+2 < len(mapping.Content) {
			end, err = yamlLineOffset(lineOffsets, mapping.Content[index+2].Line)
			if err != nil {
				return nil, err
			}
		}
		spans = append(spans, span{start: start, end: end})
	}
	if len(spans) == 0 {
		return nil, errors.New("published challenge manifest has no platform fields to remove")
	}
	slices.SortFunc(spans, func(left, right span) int { return right.start - left.start })
	result := append([]byte(nil), data...)
	for _, span := range spans {
		if span.start < 0 || span.end < span.start || span.end > len(result) {
			return nil, errors.New("published challenge manifest has invalid platform field positions")
		}
		result = append(result[:span.start], result[span.end:]...)
	}
	return result, nil
}

func isPublishedField(value string) bool {
	switch value {
	case "id", "source_slug", "image", "published_at":
		return true
	default:
		return false
	}
}

func yamlLineOffsets(data []byte) []int {
	offsets := []int{0}
	for index, value := range data {
		if value == '\n' && index+1 < len(data) {
			offsets = append(offsets, index+1)
		}
	}
	return offsets
}

func yamlLineOffset(offsets []int, line int) (int, error) {
	if line < 1 || line > len(offsets) {
		return 0, fmt.Errorf("invalid YAML line %d", line)
	}
	return offsets[line-1], nil
}
