// Package catalogseed publishes committed catalog source through the same Node
// image primitives used by the Generate Worker.
package catalogseed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	generationapp "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/challenge"
	"gopkg.in/yaml.v3"
)

// NodeImageProvider is intentionally the narrow image subset required to seed
// a committed Node challenge. It does not expose environment provisioning or
// arbitrary Incus operations to the seed command.
type NodeImageProvider interface {
	BuildNodeImage(context.Context, incus.BuildNodeImageRequest) (incus.BuildNodeImageResult, error)
	PublishNodeImage(context.Context, incus.PublishNodeImageRequest) (incus.PublishNodeImageResult, error)
	PublishChallengeNodeImage(context.Context, incus.PublishChallengeNodeImageRequest) (incus.PublishNodeImageResult, error)
	FindChallengeNodeImage(context.Context, string, string) (incus.PublishNodeImageResult, bool, error)
	DeleteCandidateNodeImage(context.Context, string, string) error
	DeleteBuildNodeImage(context.Context, incus.BuildNodeImageResult) error
}

type NodeOptions struct {
	ChallengeDir         string
	ReplaceExistingImage bool
}

type NodeResult struct {
	ChallengeID string
	Revision    string
	Fingerprint string
	Reused      bool
}

// PublishNode turns one committed, published Node challenge into the exact
// candidate-shaped bundle used by normal authoring. It builds, stages, and
// formally publishes the image before atomically updating the committed
// manifest fingerprint. The temporary candidate/build references are always
// removed by their exact identities after the formal alias exists.
func PublishNode(ctx context.Context, provider NodeImageProvider, options NodeOptions) (result NodeResult, err error) {
	if provider == nil {
		return NodeResult{}, errors.New("catalog seed requires a Node image provider")
	}
	source, err := challenge.ValidateDir(options.ChallengeDir)
	if err != nil {
		return NodeResult{}, fmt.Errorf("validate published catalog challenge: %w", err)
	}
	if source.Runtime != challenge.RuntimeNode {
		return NodeResult{}, fmt.Errorf("catalog seed supports only runtime %q", challenge.RuntimeNode)
	}

	files, revision, cleanup, err := candidateBundle(options.ChallengeDir)
	if err != nil {
		return NodeResult{}, err
	}
	defer cleanup()

	result = NodeResult{ChallengeID: source.ID, Revision: revision}
	if published, found, findErr := provider.FindChallengeNodeImage(ctx, source.ID, revision); findErr != nil {
		return NodeResult{}, fmt.Errorf("find published catalog image: %w", findErr)
	} else if found {
		if source.Image != published.Fingerprint {
			if err := updatePublishedImage(options.ChallengeDir, published.Fingerprint); err != nil {
				return NodeResult{}, err
			}
		}
		result.Fingerprint = published.Fingerprint
		result.Reused = true
		return result, nil
	}

	digest := strings.TrimPrefix(revision, "sha256:")
	candidateID := "catalog-" + digest[:24]
	workflowID := "catalog-build-" + digest[:24]
	build, err := provider.BuildNodeImage(ctx, incus.BuildNodeImageRequest{
		WorkflowID: workflowID,
		Attempt:    1,
		Revision:   revision,
		Files:      files,
	})
	if err != nil {
		return NodeResult{}, fmt.Errorf("build catalog Node image: %w", err)
	}
	defer func() {
		if cleanupErr := provider.DeleteBuildNodeImage(ctx, build); cleanupErr != nil && err == nil {
			err = fmt.Errorf("clean catalog Node build image: %w", cleanupErr)
		}
	}()

	staging, err := provider.PublishNodeImage(ctx, incus.PublishNodeImageRequest{
		CandidateRevisionID: candidateID,
		Revision:            revision,
		Build:               build,
	})
	if err != nil {
		return NodeResult{}, fmt.Errorf("publish catalog Node staging image: %w", err)
	}
	defer func() {
		if cleanupErr := provider.DeleteCandidateNodeImage(ctx, candidateID, staging.Fingerprint); cleanupErr != nil && err == nil {
			err = fmt.Errorf("clean catalog Node staging image: %w", cleanupErr)
		}
	}()

	publish := incus.PublishChallengeNodeImageRequest{
		CandidateRevisionID: candidateID,
		ChallengeID:         source.ID,
		Staging:             staging,
	}
	if options.ReplaceExistingImage {
		publish.ExpectedCurrentFingerprint = source.Image
	}
	formal, err := provider.PublishChallengeNodeImage(ctx, publish)
	if err != nil {
		return NodeResult{}, fmt.Errorf("publish catalog Node challenge image: %w", err)
	}
	if source.Image != formal.Fingerprint {
		if err := updatePublishedImage(options.ChallengeDir, formal.Fingerprint); err != nil {
			return NodeResult{}, err
		}
	}
	result.Fingerprint = formal.Fingerprint
	return result, nil
}

func candidateBundle(source string) ([]incus.ImageFile, string, func(), error) {
	root, err := os.MkdirTemp("", "breakfix-catalog-seed-")
	if err != nil {
		return nil, "", nil, fmt.Errorf("create catalog candidate staging: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	if err := challenge.CopyRegularFiles(source, root); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("copy catalog candidate staging: %w", err)
	}
	if err := removePlatformFields(filepath.Join(root, "challenge.yaml")); err != nil {
		cleanup()
		return nil, "", nil, err
	}
	if _, err := generationapp.ValidateCandidateDir(root); err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("validate catalog candidate bundle: %w", err)
	}
	files, err := incus.ImageFilesFromDirectory(root)
	if err != nil {
		cleanup()
		return nil, "", nil, fmt.Errorf("read catalog candidate bundle: %w", err)
	}
	return files, bundleRevision(files), cleanup, nil
}

func bundleRevision(files []incus.ImageFile) string {
	ordered := append([]incus.ImageFile(nil), files...)
	slices.SortFunc(ordered, func(left, right incus.ImageFile) int {
		return strings.Compare(left.Path, right.Path)
	})
	hash := sha256.New()
	for _, file := range ordered {
		_, _ = hash.Write([]byte(file.Path))
		_, _ = hash.Write([]byte{0})
		_, _ = fmt.Fprintf(hash, "%04o", file.Mode)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(file.Content)
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func removePlatformFields(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read catalog challenge manifest: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse catalog challenge manifest: %w", err)
	}
	mapping, err := documentMapping(&document)
	if err != nil {
		return err
	}
	without := make([]*yaml.Node, 0, len(mapping.Content))
	for index := 0; index < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Value == "id" || key.Value == "source_slug" || key.Value == "image" || key.Value == "published_at" {
			continue
		}
		without = append(without, key, mapping.Content[index+1])
	}
	mapping.Content = without
	normalized, err := yaml.Marshal(&document)
	if err != nil {
		return fmt.Errorf("marshal catalog candidate manifest: %w", err)
	}
	//nolint:gosec // Published catalog manifests are intentionally world-readable.
	if err := os.WriteFile(path, normalized, 0o644); err != nil {
		return fmt.Errorf("write catalog candidate manifest: %w", err)
	}
	return nil
}

func updatePublishedImage(challengeDir, image string) error {
	path := filepath.Join(challengeDir, "challenge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read published challenge manifest: %w", err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse published challenge manifest: %w", err)
	}
	mapping, err := documentMapping(&document)
	if err != nil {
		return err
	}
	var imageNode *yaml.Node
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == "image" {
			imageNode = mapping.Content[index+1]
			break
		}
	}
	if imageNode == nil {
		return errors.New("published challenge manifest has no image field")
	}
	if imageNode.Value == image {
		return nil
	}
	replacement, err := renderedScalar(imageNode.Style, image)
	if err != nil {
		return err
	}
	offset, err := yamlOffset(data, imageNode.Line, imageNode.Column)
	if err != nil {
		return fmt.Errorf("locate published challenge image: %w", err)
	}
	current, err := renderedScalar(imageNode.Style, imageNode.Value)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(data[offset:], current) {
		return errors.New("published challenge image scalar does not match YAML source")
	}
	normalized := make([]byte, 0, len(data)-len(current)+len(replacement))
	normalized = append(normalized, data[:offset]...)
	normalized = append(normalized, replacement...)
	normalized = append(normalized, data[offset+len(current):]...)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat published challenge manifest: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".challenge-*.yaml")
	if err != nil {
		return fmt.Errorf("create published challenge manifest temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	if _, err := temporary.Write(normalized); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write published challenge manifest temporary file: %w", err)
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set published challenge manifest mode: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync published challenge manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close published challenge manifest temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace published challenge manifest: %w", err)
	}
	return nil
}

func renderedScalar(style yaml.Style, value string) ([]byte, error) {
	switch style {
	case 0:
		return []byte(value), nil
	case yaml.SingleQuotedStyle:
		return []byte("'" + strings.ReplaceAll(value, "'", "''") + "'"), nil
	case yaml.DoubleQuotedStyle:
		return []byte(strconv.Quote(value)), nil
	default:
		return nil, errors.New("published challenge image must use a plain or quoted scalar")
	}
}

func yamlOffset(data []byte, line, column int) (int, error) {
	if line < 1 || column < 1 {
		return 0, errors.New("YAML scalar has no source position")
	}
	offset := 0
	for currentLine := 1; currentLine < line; currentLine++ {
		next := bytes.IndexByte(data[offset:], '\n')
		if next < 0 {
			return 0, errors.New("YAML scalar line is outside the source")
		}
		offset += next + 1
	}
	offset += column - 1
	if offset > len(data) {
		return 0, errors.New("YAML scalar column is outside the source")
	}
	return offset, nil
}

func documentMapping(document *yaml.Node) (*yaml.Node, error) {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode || len(document.Content[0].Content)%2 != 0 {
		return nil, errors.New("challenge manifest must be a YAML mapping")
	}
	return document.Content[0], nil
}
