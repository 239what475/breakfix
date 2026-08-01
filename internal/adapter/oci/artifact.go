package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	emptyJSONMediaType = "application/vnd.oci.empty.v1+json"
	emptyJSONPayload   = "{}"
)

// Artifact is a generic OCI 1.1 artifact payload. It is encoded as an OCI
// image manifest with an artifactType, an empty config and arbitrary layers.
type Artifact struct {
	ArtifactType string
	Blobs        []ArtifactBlob
	Annotations  map[string]string
}

type ArtifactBlob struct {
	MediaType   string
	Data        []byte
	Annotations map[string]string
}

type ArtifactManifest struct {
	ArtifactType string
	Blobs        []ArtifactDescriptor
	Annotations  map[string]string
}

type ArtifactDescriptor struct {
	MediaType   string
	Digest      string
	Size        int64
	Annotations map[string]string
}

// WriteArtifactArchive writes a deterministic OCI artifact layout tar. The
// caller owns artifact-specific source validation; this adapter only encodes
// bytes as a Registry-distributable OCI artifact.
func WriteArtifactArchive(destination string, artifact Artifact) (string, error) {
	if strings.TrimSpace(destination) == "" {
		return "", errors.New("OCI artifact destination is required")
	}
	if strings.TrimSpace(artifact.ArtifactType) == "" {
		return "", errors.New("OCI artifact type is required")
	}
	if len(artifact.Blobs) == 0 {
		return "", errors.New("OCI artifact requires at least one blob")
	}
	root, err := os.MkdirTemp("", "breakfix-oci-artifact-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0o755); err != nil {
		return "", fmt.Errorf("create OCI artifact blob directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0o600); err != nil {
		return "", err
	}
	emptyConfig := []byte(emptyJSONPayload)
	emptyConfigDigest := digestBytes(emptyConfig)
	if err := writeOCIBlob(root, emptyConfigDigest, emptyConfig); err != nil {
		return "", err
	}

	blobs := make([]ociDescriptor, 0, len(artifact.Blobs))
	for _, blob := range artifact.Blobs {
		if strings.TrimSpace(blob.MediaType) == "" {
			return "", errors.New("OCI artifact blob media type is required")
		}
		digest := digestBytes(blob.Data)
		if err := writeOCIBlob(root, digest, blob.Data); err != nil {
			return "", err
		}
		blobs = append(blobs, ociDescriptor{
			MediaType: blob.MediaType, Digest: digest, Size: int64(len(blob.Data)), Annotations: cloneAnnotations(blob.Annotations),
		})
	}
	manifest, err := json.Marshal(ociManifest{
		SchemaVersion: 2,
		MediaType:     ociManifestMediaType,
		ArtifactType:  artifact.ArtifactType,
		Config: ociDescriptor{
			MediaType: emptyJSONMediaType,
			Digest:    emptyConfigDigest,
			Size:      int64(len(emptyConfig)),
		},
		Layers:      blobs,
		Annotations: cloneAnnotations(artifact.Annotations),
	})
	if err != nil {
		return "", fmt.Errorf("marshal OCI artifact manifest: %w", err)
	}
	digest := digestBytes(manifest)
	if err := writeOCIBlob(root, digest, manifest); err != nil {
		return "", err
	}
	index, err := json.Marshal(ociIndex{SchemaVersion: 2, Manifests: []ociDescriptor{{
		MediaType: ociManifestMediaType, Digest: digest, Size: int64(len(manifest)),
	}}})
	if err != nil {
		return "", fmt.Errorf("marshal OCI artifact index: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), index, 0o600); err != nil {
		return "", err
	}
	if err := writeOCITar(root, destination); err != nil {
		return "", err
	}
	return digest, nil
}

// ReadArtifactArchive validates and reads a generic OCI artifact manifest.
func ReadArtifactArchive(archivePath string) (ArtifactManifest, error) {
	root, err := os.MkdirTemp("", "breakfix-oci-artifact-read-*")
	if err != nil {
		return ArtifactManifest{}, err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	if err := ExtractOCIArchive(archivePath, root); err != nil {
		return ArtifactManifest{}, err
	}
	return readArtifactLayout(root)
}

// ReadArtifactBlob returns one validated artifact blob. It is intentionally
// archive-based so callers do not receive an unchecked filesystem path.
func ReadArtifactBlob(archivePath, digest string) ([]byte, error) {
	if err := validateDigest(digest); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "breakfix-oci-artifact-blob-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	if err := ExtractOCIArchive(archivePath, root); err != nil {
		return nil, err
	}
	manifest, err := readArtifactLayout(root)
	if err != nil {
		return nil, err
	}
	for _, blob := range manifest.Blobs {
		if blob.Digest != digest {
			continue
		}
		data, err := os.ReadFile(ociBlobPath(root, digest))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != blob.Size || digest != digestBytes(data) {
			return nil, fmt.Errorf("OCI artifact blob %s digest or size mismatch", digest)
		}
		return data, nil
	}
	return nil, fmt.Errorf("OCI artifact blob %s is not in the manifest", digest)
}

func readArtifactLayout(root string) (ArtifactManifest, error) {
	descriptor, err := loadOCIRootDescriptor(root)
	if err != nil {
		return ArtifactManifest{}, err
	}
	if descriptor.MediaType != ociManifestMediaType {
		return ArtifactManifest{}, fmt.Errorf("OCI root manifest media type is %q, not an artifact manifest", descriptor.MediaType)
	}
	data, err := os.ReadFile(ociBlobPath(root, descriptor.Digest))
	if err != nil {
		return ArtifactManifest{}, fmt.Errorf("read OCI artifact manifest: %w", err)
	}
	if int64(len(data)) != descriptor.Size || descriptor.Digest != digestBytes(data) {
		return ArtifactManifest{}, errors.New("OCI artifact manifest digest or size mismatch")
	}
	var manifest ociManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ArtifactManifest{}, fmt.Errorf("parse OCI artifact manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 || manifest.MediaType != ociManifestMediaType || strings.TrimSpace(manifest.ArtifactType) == "" || len(manifest.Layers) == 0 {
		return ArtifactManifest{}, errors.New("invalid OCI artifact manifest")
	}
	if err := validateEmptyArtifactConfig(root, manifest.Config); err != nil {
		return ArtifactManifest{}, err
	}
	result := ArtifactManifest{ArtifactType: manifest.ArtifactType, Annotations: cloneAnnotations(manifest.Annotations), Blobs: make([]ArtifactDescriptor, 0, len(manifest.Layers))}
	for _, blob := range manifest.Layers {
		if strings.TrimSpace(blob.MediaType) == "" || blob.Size < 0 {
			return ArtifactManifest{}, errors.New("invalid OCI artifact blob descriptor")
		}
		if err := validateDigest(blob.Digest); err != nil {
			return ArtifactManifest{}, err
		}
		data, err := os.ReadFile(ociBlobPath(root, blob.Digest))
		if err != nil {
			return ArtifactManifest{}, fmt.Errorf("read OCI artifact blob %s: %w", blob.Digest, err)
		}
		if int64(len(data)) != blob.Size || blob.Digest != digestBytes(data) {
			return ArtifactManifest{}, fmt.Errorf("OCI artifact blob %s digest or size mismatch", blob.Digest)
		}
		result.Blobs = append(result.Blobs, ArtifactDescriptor{
			MediaType: blob.MediaType, Digest: blob.Digest, Size: blob.Size, Annotations: cloneAnnotations(blob.Annotations),
		})
	}
	return result, nil
}

func validateEmptyArtifactConfig(root string, descriptor ociDescriptor) error {
	if descriptor.MediaType != emptyJSONMediaType || descriptor.Size != int64(len(emptyJSONPayload)) || descriptor.Digest != digestBytes([]byte(emptyJSONPayload)) {
		return errors.New("invalid OCI artifact empty config")
	}
	data, err := os.ReadFile(ociBlobPath(root, descriptor.Digest))
	if err != nil {
		return fmt.Errorf("read OCI artifact empty config: %w", err)
	}
	if string(data) != emptyJSONPayload {
		return errors.New("invalid OCI artifact empty config")
	}
	return nil
}

func cloneAnnotations(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
