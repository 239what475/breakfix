package mcpconnector

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
)

const reviewBundleSchemaVersion = 1

// ReviewProjection is the local, read-only location of one immutable Server
// review snapshot. It deliberately excludes all workspace and provider facts.
type ReviewProjection struct {
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	Kind                string `json:"kind"`
	ReviewPath          string `json:"review_path"`
}

// ReviewProjector materializes immutable review bundles beneath a private
// local cache. The directory is disposable: the Server remains authoritative.
type ReviewProjector struct {
	root string
}

func NewReviewProjector(root string) (*ReviewProjector, error) {
	if strings.TrimSpace(root) == "" {
		root = filepath.Join(os.TempDir(), "breakfix", "reviews")
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return nil, errors.New("MCP review root must be an absolute path")
	}
	return &ReviewProjector{root: root}, nil
}

func (p *ReviewProjector) Root() string {
	if p == nil {
		return ""
	}
	return p.root
}

// Project validates a remote bundle before atomically publishing its local,
// read-only projection. Existing matching revisions are returned unchanged;
// a deleted projection is reconstructed from the Server bundle.
func (p *ReviewProjector) Project(bundle api.GeneratorReviewBundle) (ReviewProjection, error) {
	if p == nil || strings.TrimSpace(p.root) == "" {
		return ReviewProjection{}, errors.New("MCP review projector is not configured")
	}
	manifest := bundle.Manifest
	kind, version, err := validateReviewManifest(manifest)
	if err != nil {
		return ReviewProjection{}, err
	}
	payload, err := base64.StdEncoding.DecodeString(strings.TrimSpace(bundle.Payload))
	if err != nil {
		return ReviewProjection{}, fmt.Errorf("decode review payload: %w", err)
	}
	if reviewPayloadDigest(payload) != manifest.PayloadSha256 {
		return ReviewProjection{}, errors.New("review payload digest does not match manifest")
	}

	if err := secureMkdirAll(p.root, 0o700); err != nil {
		return ReviewProjection{}, fmt.Errorf("create review root: %w", err)
	}
	parent := filepath.Join(p.root, manifest.WorkflowId, kind)
	if err := secureMkdirAll(parent, 0o700); err != nil {
		return ReviewProjection{}, fmt.Errorf("create review parent: %w", err)
	}
	target := filepath.Join(parent, version)
	projection := ReviewProjection{
		WorkflowID:          manifest.WorkflowId,
		CandidateRevisionID: manifest.CandidateRevisionId,
		Kind:                kind,
		ReviewPath:          target,
	}

	if existing, err := readMatchingProjection(target, manifest); err != nil {
		return ReviewProjection{}, err
	} else if existing {
		return projection, nil
	}

	stage, err := os.MkdirTemp(parent, "."+version+".tmp-")
	if err != nil {
		return ReviewProjection{}, fmt.Errorf("create review projection temporary directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := extractReviewPayload(stage, kind, payload); err != nil {
		return ReviewProjection{}, err
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ReviewProjection{}, fmt.Errorf("encode review manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	if err := writeProjectionFile(stage, "manifest.json", manifestData); err != nil {
		return ReviewProjection{}, err
	}
	if err := makeProjectionReadOnly(stage); err != nil {
		return ReviewProjection{}, err
	}
	if err := os.Rename(stage, target); err != nil {
		if existing, checkErr := readMatchingProjection(target, manifest); checkErr != nil {
			return ReviewProjection{}, checkErr
		} else if existing {
			return projection, nil
		}
		return ReviewProjection{}, fmt.Errorf("publish review projection: %w", err)
	}
	return projection, nil
}

func validateReviewManifest(manifest api.GeneratorReviewManifest) (kind, version string, _ error) {
	if manifest.SchemaVersion != reviewBundleSchemaVersion {
		return "", "", fmt.Errorf("unsupported review manifest schema version %d", manifest.SchemaVersion)
	}
	if !safeReviewID(manifest.WorkflowId) || !safeReviewID(manifest.CandidateRevisionId) {
		return "", "", errors.New("review manifest has an invalid workflow or candidate revision ID")
	}
	if !validReviewSHA256(manifest.CandidateArchiveDigest) || !validReviewSHA256(manifest.PayloadSha256) {
		return "", "", errors.New("review manifest has an invalid SHA-256 digest")
	}
	if manifest.ExportedAt.IsZero() {
		return "", "", errors.New("review manifest export time is required")
	}
	switch string(manifest.Kind) {
	case "content":
		if manifest.WorkflowState != "NeedsAuthorReview" {
			return "", "", errors.New("content review manifest is not bound to content review state")
		}
		return "content", manifest.CandidateRevisionId, nil
	default:
		return "", "", errors.New("review manifest kind must be content")
	}
}

func safeReviewID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 200 {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			continue
		}
		if character == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

func validReviewSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func reviewPayloadDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readMatchingProjection(target string, manifest api.GeneratorReviewManifest) (bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect existing review projection: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, errors.New("existing review projection is not a directory")
	}
	data, err := os.ReadFile(filepath.Join(target, "manifest.json"))
	if err != nil {
		return false, fmt.Errorf("read existing review manifest: %w", err)
	}
	var existing api.GeneratorReviewManifest
	if err := json.Unmarshal(data, &existing); err != nil {
		return false, fmt.Errorf("parse existing review manifest: %w", err)
	}
	if !sameReviewManifest(existing, manifest) {
		return false, errors.New("existing review projection belongs to a different immutable snapshot")
	}
	return true, nil
}

func sameReviewManifest(left, right api.GeneratorReviewManifest) bool {
	return left.SchemaVersion == right.SchemaVersion &&
		left.Kind == right.Kind &&
		left.WorkflowId == right.WorkflowId &&
		left.WorkflowState == right.WorkflowState &&
		left.CandidateRevisionId == right.CandidateRevisionId &&
		left.CandidateArchiveDigest == right.CandidateArchiveDigest &&
		left.PayloadSha256 == right.PayloadSha256
}

func extractReviewPayload(root, kind string, payload []byte) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("open review payload: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	seen := map[string]struct{}{}
	entries := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read review payload: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return errors.New("review payload contains a non-regular file")
		}
		name, err := safeReviewPayloadPath(kind, header.Name)
		if err != nil {
			return err
		}
		if _, found := seen[name]; found {
			return fmt.Errorf("review payload duplicates %q", name)
		}
		seen[name] = struct{}{}
		if err := writeReviewTarFile(root, name, tarReader); err != nil {
			return err
		}
		entries++
	}
	if entries == 0 {
		return errors.New("review payload is empty")
	}
	return nil
}

func safeReviewPayloadPath(kind, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("review payload path is invalid: %q", value)
	}
	if value == "manifest.json" {
		return "", errors.New("review payload must not contain manifest.json")
	}
	if kind == "content" {
		if value == "overview.md" || value == "judge.md" ||
			strings.HasPrefix(value, "checkpoints/") || strings.HasPrefix(value, "candidate/") || strings.HasPrefix(value, "diff/") {
			return value, nil
		}
		return "", fmt.Errorf("content review payload path is not allowed: %q", value)
	}
	return "", fmt.Errorf("review payload path is not allowed: %q", value)
}

func writeReviewTarFile(root, name string, reader io.Reader) error {
	parent := filepath.Dir(filepath.Join(root, filepath.FromSlash(name)))
	if err := secureMkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create review payload directory: %w", err)
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create review payload file %s: %w", name, err)
	}
	if _, err := io.Copy(file, reader); err != nil {
		_ = file.Close()
		return fmt.Errorf("write review payload file %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close review payload file %s: %w", name, err)
	}
	return nil
}

func writeProjectionFile(root, name string, data []byte) error {
	if _, err := safeReviewPayloadPath("content", name); err != nil && name != "manifest.json" {
		return err
	}
	path := filepath.Join(root, filepath.FromSlash(name))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("write review projection file %s: %w", name, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write review projection file %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close review projection file %s: %w", name, err)
	}
	return nil
}

func makeProjectionReadOnly(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("review projection contains a symbolic link")
		}
		if entry.IsDir() {
			// Keep the cache removable with ordinary user permissions. The files
			// themselves are read-only, and the connector never reads this tree
			// as candidate input.
			return os.Chmod(path, 0o700)
		}
		if !entry.Type().IsRegular() {
			return errors.New("review projection contains a non-regular file")
		}
		return os.Chmod(path, 0o444)
	})
}

func secureMkdirAll(directory string, mode fs.FileMode) error {
	directory = filepath.Clean(directory)
	if !filepath.IsAbs(directory) {
		return errors.New("directory must be absolute")
	}
	volume := filepath.VolumeName(directory)
	current := volume + string(filepath.Separator)
	remaining := strings.TrimPrefix(directory, current)
	for _, part := range strings.Split(remaining, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, mode); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
	}
	return nil
}
