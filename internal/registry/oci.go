package registry

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	ociManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	ociIndexMediaType    = "application/vnd.oci.image.index.v1+json"
)

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	Manifests     []ociDescriptor `json:"manifests"`
}

type ociManifest struct {
	Config    ociDescriptor   `json:"config"`
	Layers    []ociDescriptor `json:"layers"`
	Manifests []ociDescriptor `json:"manifests"`
}

// PullOCIArchive copies a trusted Registry image into a local OCI archive.
// Server uses it to hand the fixed platform base image to an untrusted Builder
// without giving the Builder a Registry credential.
func (c Client) PullOCIArchive(ctx context.Context, imageName, destination string) error {
	if err := c.Credentials.Validate(); err != nil {
		return err
	}
	registryAddress, repository, reference, err := imageReference(imageName)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "breakfix-oci-pull-*")
	if err != nil {
		return fmt.Errorf("create OCI staging directory: %w", err)
	}
	defer os.RemoveAll(root) //nolint:errcheck
	layout := filepath.Join(root, "layout")
	if err := os.MkdirAll(filepath.Join(layout, "blobs", "sha256"), 0755); err != nil {
		return fmt.Errorf("create OCI blob directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(layout, "oci-layout"), []byte("{\"imageLayoutVersion\":\"1.0.0\"}\n"), 0644); err != nil {
		return err
	}

	rootDescriptor, err := c.pullManifest(ctx, registryAddress, repository, reference, layout)
	if err != nil {
		return err
	}
	rootDescriptor.Annotations = map[string]string{"org.opencontainers.image.ref.name": reference}
	index, err := json.Marshal(ociIndex{SchemaVersion: 2, Manifests: []ociDescriptor{rootDescriptor}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(layout, "index.json"), index, 0644); err != nil {
		return err
	}
	if err := writeOCITar(layout, destination); err != nil {
		return err
	}
	return nil
}

func (c Client) pullManifest(ctx context.Context, registryAddress, repository, reference, layout string) (ociDescriptor, error) {
	body, mediaType, digest, err := c.getManifest(ctx, registryAddress, repository, reference)
	if err != nil {
		return ociDescriptor{}, err
	}
	descriptor := ociDescriptor{MediaType: mediaType, Digest: digest, Size: int64(len(body))}
	if err := writeOCIBlob(layout, descriptor.Digest, body); err != nil {
		return ociDescriptor{}, err
	}
	var manifest ociManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return ociDescriptor{}, fmt.Errorf("parse registry manifest %s: %w", reference, err)
	}
	if len(manifest.Manifests) != 0 {
		for _, child := range manifest.Manifests {
			if _, err := c.pullManifest(ctx, registryAddress, repository, child.Digest, layout); err != nil {
				return ociDescriptor{}, err
			}
		}
		return descriptor, nil
	}
	if err := c.pullBlob(ctx, registryAddress, repository, manifest.Config, layout); err != nil {
		return ociDescriptor{}, err
	}
	for _, layer := range manifest.Layers {
		if err := c.pullBlob(ctx, registryAddress, repository, layer, layout); err != nil {
			return ociDescriptor{}, err
		}
	}
	return descriptor, nil
}

func (c Client) pullBlob(ctx context.Context, registryAddress, repository string, descriptor ociDescriptor, layout string) error {
	if err := validateDigest(descriptor.Digest); err != nil {
		return err
	}
	if _, err := os.Stat(ociBlobPath(layout, descriptor.Digest)); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.registryURL(registryAddress, "/v2/"+repositoryPath(repository)+"/blobs/"+url.PathEscape(descriptor.Digest)), nil)
	if err != nil {
		return err
	}
	c.Credentials.apply(request)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("download registry blob %s: %w", descriptor.Digest, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download registry blob %s: status %d", descriptor.Digest, response.StatusCode)
	}
	temporary, err := os.CreateTemp(filepath.Dir(ociBlobPath(layout, descriptor.Digest)), ".blob-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), response.Body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if descriptor.Digest != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
		return fmt.Errorf("registry blob %s digest mismatch", descriptor.Digest)
	}
	if err := os.Rename(temporaryPath, ociBlobPath(layout, descriptor.Digest)); err != nil {
		return err
	}
	return nil
}

func (c Client) getManifest(ctx context.Context, registryAddress, repository, reference string) ([]byte, string, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.registryURL(registryAddress, "/v2/"+repositoryPath(repository)+"/manifests/"+url.PathEscape(reference)), nil)
	if err != nil {
		return nil, "", "", err
	}
	request.Header.Set("Accept", strings.Join([]string{
		ociManifestMediaType,
		ociIndexMediaType,
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", "))
	c.Credentials.apply(request)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, "", "", fmt.Errorf("download registry manifest %s: %w", reference, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("download registry manifest %s: status %d", reference, response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, "", "", err
	}
	digest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		digest = "sha256:" + sha256Hex(body)
	}
	if err := validateDigest(digest); err != nil {
		return nil, "", "", fmt.Errorf("registry manifest digest: %w", err)
	}
	if digest != "sha256:"+sha256Hex(body) {
		return nil, "", "", fmt.Errorf("registry manifest %s digest mismatch", reference)
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType == "" {
		mediaType = ociManifestMediaType
	}
	return body, mediaType, digest, nil
}

// PushOCIArchive publishes all OCI blobs and the archive root manifest to a
// fixed target reference. The Publisher controls only this target name; it
// never receives one from untrusted archive content.
func (c Client) PushOCIArchive(ctx context.Context, imageName, archivePath string) error {
	if err := c.Credentials.Validate(); err != nil {
		return err
	}
	registryAddress, repository, reference, err := imageReference(imageName)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "breakfix-oci-push-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	if err := ExtractOCIArchive(archivePath, root); err != nil {
		return err
	}
	descriptor, err := loadOCIRootDescriptor(root)
	if err != nil {
		return err
	}
	blobs, err := listOCIBlobs(root)
	if err != nil {
		return err
	}
	for _, blob := range blobs {
		if err := c.pushBlob(ctx, registryAddress, repository, blob.digest, blob.path); err != nil {
			return err
		}
	}
	manifest, err := os.ReadFile(ociBlobPath(root, descriptor.Digest))
	if err != nil {
		return err
	}
	if descriptor.Digest != "sha256:"+sha256Hex(manifest) {
		return fmt.Errorf("OCI root manifest digest mismatch")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.registryURL(registryAddress, "/v2/"+repositoryPath(repository)+"/manifests/"+url.PathEscape(reference)), strings.NewReader(string(manifest)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", descriptor.MediaType)
	c.Credentials.apply(request)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("publish registry manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("publish registry manifest: status %d", response.StatusCode)
	}
	return nil
}

// CopyImage promotes a Registry-authenticated immutable source image to a
// platform-chosen target reference. The source is first validated as an OCI
// archive and the target name is never derived from untrusted image content.
func (c Client) CopyImage(ctx context.Context, sourceImage, targetImage string) error {
	if err := c.Credentials.Validate(); err != nil {
		return err
	}
	if _, _, _, err := imageReference(sourceImage); err != nil {
		return err
	}
	if _, _, _, err := imageReference(targetImage); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "breakfix-oci-copy-*")
	if err != nil {
		return fmt.Errorf("create OCI copy directory: %w", err)
	}
	defer os.RemoveAll(root) //nolint:errcheck

	archive := filepath.Join(root, "image.oci.tar")
	if err := c.PullOCIArchive(ctx, sourceImage, archive); err != nil {
		return fmt.Errorf("pull source image: %w", err)
	}
	if err := c.PushOCIArchive(ctx, targetImage, archive); err != nil {
		return fmt.Errorf("push target image: %w", err)
	}
	return nil
}

type ociBlob struct {
	digest string
	path   string
}

func listOCIBlobs(root string) ([]ociBlob, error) {
	entries, err := os.ReadDir(filepath.Join(root, "blobs", "sha256"))
	if err != nil {
		return nil, fmt.Errorf("read OCI blobs: %w", err)
	}
	blobs := make([]ociBlob, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("OCI blob %s is not a regular file", entry.Name())
		}
		digest := "sha256:" + entry.Name()
		if err := validateDigest(digest); err != nil {
			return nil, err
		}
		path := filepath.Join(root, "blobs", "sha256", entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if digest != "sha256:"+sha256Hex(data) {
			return nil, fmt.Errorf("OCI blob %s digest mismatch", digest)
		}
		blobs = append(blobs, ociBlob{digest: digest, path: path})
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].digest < blobs[j].digest })
	return blobs, nil
}

func (c Client) pushBlob(ctx context.Context, registryAddress, repository, digest, path string) error {
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, c.registryURL(registryAddress, "/v2/"+repositoryPath(repository)+"/blobs/"+url.PathEscape(digest)), nil)
	if err != nil {
		return err
	}
	c.Credentials.apply(head)
	response, err := c.httpClient().Do(head)
	if err != nil {
		return fmt.Errorf("check registry blob %s: %w", digest, err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("check registry blob %s: status %d", digest, response.StatusCode)
	}
	start, err := http.NewRequestWithContext(ctx, http.MethodPost, c.registryURL(registryAddress, "/v2/"+repositoryPath(repository)+"/blobs/uploads/"), nil)
	if err != nil {
		return err
	}
	c.Credentials.apply(start)
	response, err = c.httpClient().Do(start)
	if err != nil {
		return fmt.Errorf("start registry blob upload: %w", err)
	}
	location := strings.TrimSpace(response.Header.Get("Location"))
	status := response.StatusCode
	response.Body.Close()
	if status != http.StatusAccepted || location == "" {
		return fmt.Errorf("start registry blob upload: status %d", status)
	}
	uploadURL, err := c.resolveLocation(registryAddress, location)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(uploadURL)
	if err != nil {
		return err
	}
	query := parsed.Query()
	query.Set("digest", digest)
	parsed.RawQuery = query.Encode()
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	finish, err := http.NewRequestWithContext(ctx, http.MethodPut, parsed.String(), file)
	if err != nil {
		return err
	}
	c.Credentials.apply(finish)
	response, err = c.httpClient().Do(finish)
	if err != nil {
		return fmt.Errorf("finish registry blob upload: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return fmt.Errorf("finish registry blob upload: status %d", response.StatusCode)
	}
	return nil
}

// ResolveImageDigest returns the Registry-authenticated immutable reference.
func (c Client) ResolveImageDigest(ctx context.Context, imageName string) (string, error) {
	if err := c.Credentials.Validate(); err != nil {
		return "", err
	}
	registryAddress, repository, reference, err := imageReference(imageName)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, c.registryURL(registryAddress, "/v2/"+repositoryPath(repository)+"/manifests/"+url.PathEscape(reference)), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", ociManifestMediaType+", application/vnd.docker.distribution.manifest.v2+json")
	c.Credentials.apply(request)
	response, err := c.httpClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("resolve image manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve image manifest: status %d", response.StatusCode)
	}
	digest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if err := validateDigest(digest); err != nil {
		return "", fmt.Errorf("resolve image manifest: %w", err)
	}
	return registryAddress + "/" + repository + "@" + digest, nil
}

func (c Client) registryURL(registryAddress, path string) string {
	scheme := "https"
	if c.Insecure {
		scheme = "http"
	}
	return scheme + "://" + registryAddress + path
}

func (c Client) resolveLocation(registryAddress, location string) (string, error) {
	parsed, err := url.Parse(location)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		return parsed.String(), nil
	}
	return c.registryURL(registryAddress, location), nil
}

func (c Client) httpClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute}
}

func writeOCIBlob(layout, digest string, data []byte) error {
	if err := validateDigest(digest); err != nil {
		return err
	}
	if digest != "sha256:"+sha256Hex(data) {
		return fmt.Errorf("OCI blob %s digest mismatch", digest)
	}
	return os.WriteFile(ociBlobPath(layout, digest), data, 0644)
}

func ociBlobPath(root, digest string) string {
	return filepath.Join(root, "blobs", "sha256", strings.TrimPrefix(digest, "sha256:"))
}

func validateDigest(digest string) error {
	parts := strings.Split(strings.TrimSpace(digest), ":")
	if len(parts) != 2 || parts[0] != "sha256" || len(parts[1]) != 64 {
		return fmt.Errorf("unsupported OCI digest %q", digest)
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return fmt.Errorf("invalid OCI digest %q", digest)
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeOCITar(root, destination string) error {
	target, err := os.Create(destination)
	if err != nil {
		return err
	}
	defer target.Close()
	writer := tar.NewWriter(target)
	defer writer.Close()
	paths := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(paths)
	for _, relative := range paths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := writer.WriteHeader(&tar.Header{Name: relative, Mode: 0644, Size: info.Size()}); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// ExtractOCIArchive extracts a single-platform OCI archive into root. It is
// intentionally strict because Publisher receives bytes produced after
// untrusted Dockerfile execution.
func ExtractOCIArchive(archivePath, root string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read OCI archive: %w", err)
		}
		relative := filepath.Clean(filepath.FromSlash(header.Name))
		if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("OCI archive entry escapes root: %q", header.Name)
		}
		path := filepath.Join(root, relative)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("OCI archive contains unsupported entry %q", header.Name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, reader)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
}

// ValidateOCIArchive checks the archive layout and digests before any Registry
// request is made. It lets Publisher classify malformed Builder output as an
// artifact failure instead of a Registry infrastructure failure.
func ValidateOCIArchive(archivePath string) error {
	root, err := os.MkdirTemp("", "breakfix-oci-validate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	if err := ExtractOCIArchive(archivePath, root); err != nil {
		return err
	}
	if _, err := loadOCIRootDescriptor(root); err != nil {
		return err
	}
	_, err = listOCIBlobs(root)
	return err
}

// OCILayoutRootDigest returns the root descriptor digest from an extracted OCI
// image layout. The descriptor must refer to a present blob with matching
// content, so callers can safely pass the result to BuildKit's oci-layout
// named-context syntax.
func OCILayoutRootDigest(root string) (string, error) {
	descriptor, err := loadOCIRootDescriptor(root)
	if err != nil {
		return "", err
	}
	manifest, err := os.ReadFile(ociBlobPath(root, descriptor.Digest))
	if err != nil {
		return "", fmt.Errorf("read OCI root manifest: %w", err)
	}
	if descriptor.Digest != "sha256:"+sha256Hex(manifest) {
		return "", fmt.Errorf("OCI root manifest digest mismatch")
	}
	return descriptor.Digest, nil
}

func loadOCIRootDescriptor(root string) (ociDescriptor, error) {
	data, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		return ociDescriptor{}, err
	}
	var index ociIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return ociDescriptor{}, fmt.Errorf("parse OCI index: %w", err)
	}
	if index.SchemaVersion != 2 || len(index.Manifests) != 1 {
		return ociDescriptor{}, fmt.Errorf("OCI index must contain exactly one root manifest")
	}
	descriptor := index.Manifests[0]
	if err := validateDigest(descriptor.Digest); err != nil {
		return ociDescriptor{}, err
	}
	if strings.TrimSpace(descriptor.MediaType) == "" {
		return ociDescriptor{}, fmt.Errorf("OCI root manifest media type is required")
	}
	return descriptor, nil
}
