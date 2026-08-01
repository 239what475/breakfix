package challenge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

// PromoteDirectory atomically turns a verified CandidateRevision directory into a
// catalog challenge. The source directory is never modified: platform-owned
// identity and image metadata are written only to Materialize's staging copy.
func PromoteDirectory(challengesDir, sourceDir, challengeID, image, contentRevision string) (*Entry, error) {
	return PromoteDirectoryAt(challengesDir, sourceDir, challengeID, image, contentRevision, time.Now().UTC())
}

// PromoteDirectoryAt has the same publication behavior as PromoteDirectory,
// with an explicit platform publication time for deterministic callers.
func PromoteDirectoryAt(challengesDir, sourceDir, challengeID, image, contentRevision string, publishedAt time.Time) (*Entry, error) {
	if !ValidID(challengeID) {
		return nil, fmt.Errorf("invalid challenge id %q", challengeID)
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
	sourceSlug := SourceSlugFor(source.Title, challengeID)
	return MaterializeWithSlug(challengesDir, challengeID, sourceSlug, func(staging string) error {
		if err := CopyRegularFiles(sourceDir, staging); err != nil {
			return err
		}
		return writePublishedManifest(staging, challengeID, sourceSlug, image, contentRevision, publishedAt)
	})
}

func writePublishedManifest(dir, challengeID, sourceSlug, image, contentRevision string, publishedAt time.Time) error {
	manifestPath := filepath.Join(dir, "challenge.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read challenge manifest: %w", err)
	}
	var manifest Spec
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse challenge manifest: %w", err)
	}
	manifest.ID = challengeID
	manifest.SourceSlug = sourceSlug
	manifest.Image = image
	manifest.ContentRevision = contentRevision
	manifest.PublishedAt = publishedAt.UTC()
	normalized, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal challenge manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, normalized, 0600); err != nil {
		return fmt.Errorf("write challenge manifest: %w", err)
	}

	return nil
}

func SourceSlugFor(title, challengeID string) string {
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
		base = "challenge"
	}
	suffix := strings.TrimPrefix(challengeID, "chal-")
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
