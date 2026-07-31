package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const gzipLayerMediaType = "application/vnd.oci.image.layer.v1.tar+gzip"

// AppendChallengeLayer creates a new OCI archive without running candidate
// code. The trusted base config and entrypoint remain intact; the only new
// filesystem content is the deterministic challenge bundle.
func AppendChallengeLayer(baseArchive, bundleRoot, destination string) (string, error) {
	if strings.TrimSpace(baseArchive) == "" || strings.TrimSpace(bundleRoot) == "" || strings.TrimSpace(destination) == "" {
		return "", errors.New("base archive, bundle root, and destination are required")
	}
	root, err := os.MkdirTemp("", "breakfix-oci-layer-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	if err := ExtractOCIArchive(baseArchive, root); err != nil {
		return "", fmt.Errorf("extract base OCI archive: %w", err)
	}
	descriptor, err := loadOCIRootDescriptor(root)
	if err != nil {
		return "", err
	}
	manifestBytes, err := os.ReadFile(ociBlobPath(root, descriptor.Digest))
	if err != nil {
		return "", fmt.Errorf("read base OCI manifest: %w", err)
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return "", fmt.Errorf("parse base OCI manifest: %w", err)
	}
	if raw := manifest["manifests"]; len(raw) != 0 && string(raw) != "null" && string(raw) != "[]" {
		return "", errors.New("K8s base image digest must identify one image manifest, not an image index")
	}
	var configDescriptor ociDescriptor
	if err := json.Unmarshal(manifest["config"], &configDescriptor); err != nil {
		return "", fmt.Errorf("parse base OCI config descriptor: %w", err)
	}
	var layers []ociDescriptor
	if err := json.Unmarshal(manifest["layers"], &layers); err != nil {
		return "", fmt.Errorf("parse base OCI layers: %w", err)
	}
	configBytes, err := os.ReadFile(ociBlobPath(root, configDescriptor.Digest))
	if err != nil {
		return "", fmt.Errorf("read base OCI config: %w", err)
	}

	layer, diffID, err := deterministicChallengeLayer(bundleRoot)
	if err != nil {
		return "", err
	}
	layerDigest := digestBytes(layer)
	if err := writeOCIBlob(root, layerDigest, layer); err != nil {
		return "", err
	}
	layerDescriptor := ociDescriptor{MediaType: gzipLayerMediaType, Digest: layerDigest, Size: int64(len(layer))}
	layers = append(layers, layerDescriptor)

	updatedConfig, err := appendConfigDiffID(configBytes, diffID)
	if err != nil {
		return "", err
	}
	configDigest := digestBytes(updatedConfig)
	if err := writeOCIBlob(root, configDigest, updatedConfig); err != nil {
		return "", err
	}
	configDescriptor.Digest = configDigest
	configDescriptor.Size = int64(len(updatedConfig))
	manifest["config"], _ = json.Marshal(configDescriptor)
	manifest["layers"], _ = json.Marshal(layers)
	delete(manifest, "manifests")
	updatedManifest, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	manifestDigest := digestBytes(updatedManifest)
	if err := writeOCIBlob(root, manifestDigest, updatedManifest); err != nil {
		return "", err
	}
	descriptor.Digest = manifestDigest
	descriptor.Size = int64(len(updatedManifest))
	indexBytes, err := json.Marshal(ociIndex{SchemaVersion: 2, Manifests: []ociDescriptor{descriptor}})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), indexBytes, 0o600); err != nil {
		return "", err
	}
	if err := writeOCITar(root, destination); err != nil {
		return "", err
	}
	return manifestDigest, nil
}

func deterministicChallengeLayer(bundleRoot string) ([]byte, string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(bundleRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("challenge layer contains unsupported entry %s", path)
		}
		relative, err := filepath.Rel(bundleRoot, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(paths) == 0 {
		return nil, "", errors.New("challenge bundle is empty")
	}
	sort.Strings(paths)
	var uncompressed bytes.Buffer
	tarWriter := tar.NewWriter(&uncompressed)
	for _, relative := range paths {
		path := filepath.Join(bundleRoot, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			return nil, "", err
		}
		header := &tar.Header{
			Name: "opt/breakfix/challenge/" + relative, Mode: int64(info.Mode().Perm()), Size: info.Size(),
			Uid: 0, Gid: 0, ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{},
			Format: tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		if _, err := tarWriter.Write(data); err != nil {
			return nil, "", err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, "", err
	}
	diffID := digestBytes(uncompressed.Bytes())
	var compressed bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return nil, "", err
	}
	gzipWriter.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.OS = 255
	if _, err := gzipWriter.Write(uncompressed.Bytes()); err != nil {
		return nil, "", err
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, "", err
	}
	return compressed.Bytes(), diffID, nil
}

func appendConfigDiffID(data []byte, diffID string) ([]byte, error) {
	var config map[string]json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse base OCI config: %w", err)
	}
	var rootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	}
	if err := json.Unmarshal(config["rootfs"], &rootFS); err != nil {
		return nil, fmt.Errorf("parse base OCI rootfs: %w", err)
	}
	if rootFS.Type != "layers" {
		return nil, fmt.Errorf("unsupported OCI rootfs type %q", rootFS.Type)
	}
	rootFS.DiffIDs = append(rootFS.DiffIDs, diffID)
	config["rootfs"], _ = json.Marshal(rootFS)
	var history []json.RawMessage
	if raw := config["history"]; len(raw) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &history); err != nil {
			return nil, fmt.Errorf("parse base OCI history: %w", err)
		}
	}
	historyEntry, _ := json.Marshal(map[string]any{
		"created":    time.Unix(0, 0).UTC().Format(time.RFC3339),
		"created_by": "breakfix deterministic challenge layer",
	})
	history = append(history, historyEntry)
	config["history"], _ = json.Marshal(history)
	return json.Marshal(config)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
