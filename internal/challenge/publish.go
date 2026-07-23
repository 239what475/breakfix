package challenge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PromoteSubmission atomically turns a previously verified submission archive
// into a catalog challenge. Verification never calls this function; Gateway
// invokes it only after the author explicitly publishes a verified revision.
func PromoteSubmission(dataDir, challengesDir, submissionID, challengeID, image string) (*Entry, error) {
	archive, err := os.Open(SubmissionPath(dataDir, submissionID))
	if err != nil {
		return nil, fmt.Errorf("open verified artifact: %w", err)
	}
	defer archive.Close()

	staging, err := os.MkdirTemp(dataDir, ".publish-")
	if err != nil {
		return nil, fmt.Errorf("create publish staging: %w", err)
	}
	defer os.RemoveAll(staging) //nolint:errcheck
	if err := ExtractTarGz(staging, archive); err != nil {
		return nil, fmt.Errorf("extract verified artifact: %w", err)
	}
	if _, err := ValidateSubmissionDir(staging); err != nil {
		return nil, fmt.Errorf("validate verified artifact: %w", err)
	}

	return PromoteDirectory(challengesDir, staging, challengeID, image)
}

// PromoteDirectory atomically turns a verified artifact directory into a
// catalog challenge. The source directory is never modified: platform-owned
// identity and image metadata are written only to Materialize's staging copy.
func PromoteDirectory(challengesDir, sourceDir, challengeID, image string) (*Entry, error) {
	if !ValidID(challengeID) {
		return nil, fmt.Errorf("invalid challenge id %q", challengeID)
	}
	if strings.TrimSpace(image) == "" {
		return nil, fmt.Errorf("published image is empty")
	}
	if _, err := ValidateSubmissionDir(sourceDir); err != nil {
		return nil, fmt.Errorf("validate verified artifact: %w", err)
	}
	return Materialize(challengesDir, challengeID, func(staging string) error {
		if err := CopyRegularFiles(sourceDir, staging); err != nil {
			return err
		}
		return writePublishedManifest(staging, challengeID, image)
	})
}

func writePublishedManifest(dir, challengeID, image string) error {
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
	manifest.Image = image
	if strings.TrimSpace(manifest.Type) == "" {
		manifest.Type = TypeScript
	}
	normalized, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal challenge manifest: %w", err)
	}
	if err := os.WriteFile(manifestPath, normalized, 0644); err != nil {
		return fmt.Errorf("write challenge manifest: %w", err)
	}

	return nil
}

// CopyRegularFiles copies an artifact without following symlinks. It is shared
// by publishing and tests that need the same archive-safety contract.
func CopyRegularFiles(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact does not allow symlink %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact does not allow non-regular file %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
