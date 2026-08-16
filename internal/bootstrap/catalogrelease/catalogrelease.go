// Package catalogrelease assembles the Catalog Release packaging command.
package catalogrelease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
)

type Options struct {
	Source                string
	Output                string
	Reference             string
	TrustBundleFile       string
	PrintContentRevisions bool
}

// Run writes a portable Catalog Release OCI archive and optionally publishes
// it to the explicitly configured Registry. It never contacts Server, Incus
// or Kubernetes.
func Run(ctx context.Context, options Options) (string, error) {
	if options.PrintContentRevisions {
		report, err := appcatalog.CalculateContentRevisions(strings.TrimSpace(options.Source))
		if err != nil {
			return "", fmt.Errorf("calculate catalog content revisions: %w", err)
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", fmt.Errorf("encode catalog content revisions: %w", err)
		}
		return string(encoded), nil
	}
	if strings.TrimSpace(options.Output) == "" {
		return "", errors.New("-output is required")
	}
	bundle, err := appcatalog.BuildPortableBundle(strings.TrimSpace(options.Source))
	if err != nil {
		return "", fmt.Errorf("build catalog release bundle: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(options.Output), 0o755); err != nil {
		return "", fmt.Errorf("create bundle output directory: %w", err)
	}
	digest, err := oci.WriteArtifactArchive(options.Output, oci.Artifact{
		ArtifactType: bundle.ArtifactType,
		Blobs:        []oci.ArtifactBlob{{MediaType: bundle.LayerMediaType, Data: bundle.SourceLayer}},
		Annotations:  bundle.Annotations,
	})
	if err != nil {
		return "", fmt.Errorf("write catalog release OCI archive: %w", err)
	}

	if strings.TrimSpace(options.Reference) == "" {
		return digest, nil
	}
	immutable, err := immutableReference(options.Reference, digest)
	if err != nil {
		return "", err
	}
	authority, err := oci.AuthorityForReference(options.Reference)
	if err != nil {
		return "", fmt.Errorf("derive Registry authority: %w", err)
	}
	trustBundle := strings.TrimSpace(options.TrustBundleFile)
	if trustBundle == "" {
		trustBundle = strings.TrimSpace(os.Getenv("BREAKFIX_REGISTRY_TRUST_BUNDLE_FILE"))
	}
	client, err := oci.NewClient(oci.ClientOptions{
		Authority: authority,
		Credentials: oci.Credentials{
			Username: os.Getenv("BREAKFIX_REGISTRY_USERNAME"),
			Password: os.Getenv("BREAKFIX_REGISTRY_PASSWORD"),
		},
		TrustBundleFile: trustBundle,
	})
	if err != nil {
		return "", fmt.Errorf("create Registry client: %w", err)
	}
	if err := client.PushOCIArchive(ctx, strings.TrimSpace(options.Reference), options.Output); err != nil {
		return "", fmt.Errorf("push catalog release OCI artifact: %w", err)
	}
	return immutable, nil
}

func immutableReference(reference, digest string) (string, error) {
	reference = strings.TrimSpace(reference)
	if strings.Contains(reference, "@") {
		return "", errors.New("catalog release publish reference must use a mutable tag, not a digest")
	}
	authority, repository, found := strings.Cut(reference, "/")
	if !found || strings.TrimSpace(authority) == "" || strings.TrimSpace(repository) == "" {
		return "", fmt.Errorf("catalog release reference %q must include Registry authority and repository", reference)
	}
	if lastColon := strings.LastIndex(repository, ":"); lastColon >= 0 {
		if strings.TrimSpace(repository[lastColon+1:]) == "" {
			return "", fmt.Errorf("catalog release reference %q has an empty tag", reference)
		}
		repository = repository[:lastColon]
	}
	if strings.TrimSpace(repository) == "" {
		return "", fmt.Errorf("catalog release reference %q has an empty repository", reference)
	}
	return authority + "/" + repository + "@" + digest, nil
}
