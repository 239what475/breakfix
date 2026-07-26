package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeleteImage removes an image by digest. Registry V2 does not reliably
// support deleting a tag directly, so resolve the tag's manifest first.
func DeleteImage(ctx context.Context, imageName string, insecure bool) error {
	registry, repository, reference, err := imageReference(imageName)
	if err != nil {
		return err
	}
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	manifestURL := scheme + "://" + registry + "/v2/" + repositoryPath(repository) + "/manifests/" + url.PathEscape(reference)
	client := &http.Client{Timeout: 20 * time.Second}

	head, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return fmt.Errorf("create manifest request: %w", err)
	}
	head.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	response, err := client.Do(head)
	if err != nil {
		return fmt.Errorf("resolve image manifest: %w", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("resolve image manifest: status %d", response.StatusCode)
	}
	digest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return fmt.Errorf("resolve image manifest: registry did not return Docker-Content-Digest")
	}

	remove, err := http.NewRequestWithContext(ctx, http.MethodDelete, scheme+"://"+registry+"/v2/"+repositoryPath(repository)+"/manifests/"+url.PathEscape(digest), nil)
	if err != nil {
		return fmt.Errorf("create image delete request: %w", err)
	}
	response, err = client.Do(remove)
	if err != nil {
		return fmt.Errorf("delete image manifest: %w", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete image manifest: status %d", response.StatusCode)
}

func imageReference(imageName string) (registry, repository, reference string, err error) {
	parts := strings.SplitN(strings.TrimSpace(imageName), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", "", fmt.Errorf("invalid registry image %q", imageName)
	}
	registry, repository = parts[0], parts[1]
	if index := strings.LastIndex(repository, "@"); index >= 0 {
		reference = repository[index+1:]
		repository = repository[:index]
	} else if index := strings.LastIndex(repository, ":"); index > strings.LastIndex(repository, "/") {
		reference = repository[index+1:]
		repository = repository[:index]
	} else {
		reference = "latest"
	}
	if strings.TrimSpace(repository) == "" || strings.TrimSpace(reference) == "" {
		return "", "", "", fmt.Errorf("invalid registry image %q", imageName)
	}
	return registry, repository, reference, nil
}

func repositoryPath(repository string) string {
	parts := strings.Split(repository, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
