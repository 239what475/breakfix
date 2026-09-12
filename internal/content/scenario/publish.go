package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// PromoteDirectoryAt atomically turns one verified source directory into a
// materialized immutable Scenario revision. The source directory is never
// modified: platform-owned identity and artifact metadata are written only to
// Materialize's staging copy.
func PromoteDirectoryAt(scenariosDir, sourceDir, scenarioID, revisionID, sourceSlug, image, contentRevision string, publishedAt time.Time) (*Entry, error) {
	if !ValidID(scenarioID) {
		return nil, fmt.Errorf("invalid scenario id %q", scenarioID)
	}
	if !ValidRevisionID(revisionID) {
		return nil, fmt.Errorf("invalid scenario revision id %q", revisionID)
	}
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("published image is empty")
	}
	if !contentRevisionPattern.MatchString(contentRevision) {
		return nil, fmt.Errorf("published content revision must be a lowercase sha256 digest")
	}
	if publishedAt.IsZero() {
		return nil, fmt.Errorf("published time is required")
	}
	source, err := ValidateCandidateDir(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("validate verified artifact: %w", err)
	}
	if sourceSlug == "" {
		sourceSlug = SourceSlugFor(source.Title, scenarioID)
	}
	if !ValidSourceSlug(sourceSlug) {
		return nil, fmt.Errorf("invalid scenario source slug %q", sourceSlug)
	}
	directoryName := MaterializedPath(sourceSlug, revisionID)
	return MaterializeWithPath(scenariosDir, scenarioID, revisionID, directoryName, func(staging string) error {
		if err := CopyRegularFiles(sourceDir, staging); err != nil {
			return err
		}
		return writePublishedManifest(staging, scenarioID, revisionID, sourceSlug, image, contentRevision, publishedAt)
	})
}

func writePublishedManifest(dir, scenarioID, revisionID, sourceSlug, image, contentRevision string, publishedAt time.Time) error {
	manifestPath := filepath.Join(dir, "scenario.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read scenario manifest: %w", err)
	}
	var manifest Spec
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse scenario manifest: %w", err)
	}
	manifest.ID = scenarioID
	manifest.RevisionID = revisionID
	manifest.SourceSlug = sourceSlug
	manifest.Image = image
	manifest.ContentRevision = contentRevision
	manifest.PublishedAt = publishedAt.UTC()
	manifest.Type = NormalizeScenarioType(string(manifest.Type))
	manifest.Tags, err = NormalizeTags(manifest.Tags)
	if err != nil {
		return fmt.Errorf("normalize scenario tags: %w", err)
	}
	normalized, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal scenario manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, normalized, 0600); err != nil {
		return fmt.Errorf("write scenario manifest: %w", err)
	}

	return nil
}

func SourceSlugFor(title, scenarioID string) string {
	var builder strings.Builder
	separator := true
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			builder.WriteRune(r)
			separator = false
			continue
		}
		if !separator {
			builder.WriteByte('-')
			separator = true
		}
	}
	base := strings.Trim(builder.String(), "-")
	if base == "" {
		base = "scenario"
	}
	suffix := strings.TrimPrefix(scenarioID, "chal-")
	suffix = strings.Trim(suffix, "-")
	if suffix == "" {
		suffix = "id"
	}
	if len(suffix) > 8 {
		suffix = strings.TrimRight(suffix[:8], "-")
		if suffix == "" {
			suffix = "id"
		}
	}
	return base + "-" + suffix
}

// CopyRegularFiles copies an artifact without following symlinks. It is shared
// by publishing and tests that need the same archive-safety contract.
func CopyRegularFiles(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	sourceRoot, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer func() { _ = sourceRoot.Close() }()
	destinationRoot, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = destinationRoot.Close() }()
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact does not allow symlink %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return destinationRoot.MkdirAll(rel, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact does not allow non-regular file %s", path)
		}
		data, err := sourceRoot.ReadFile(rel)
		if err != nil {
			return err
		}
		return destinationRoot.WriteFile(rel, data, info.Mode().Perm())
	})
}
