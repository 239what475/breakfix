// catalog-release packages a portable CatalogRelease source as an OCI
// artifact and can push that artifact to an operator-provided Registry. It
// never installs a release: installation remains the Server's admin API.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
)

func main() {
	source := flag.String("source", "catalog", "portable CatalogRelease source directory")
	output := flag.String("output", "", "destination OCI archive path")
	reference := flag.String("reference", "", "optional mutable OCI reference to publish, for example registry.example/catalog/foundation:2026.08.01")
	endpoint := flag.String("registry-endpoint", "", "optional HTTPS Registry authority; defaults to the reference authority")
	trustBundle := flag.String("trust-bundle-file", os.Getenv("BREAKFIX_REGISTRY_TRUST_BUNDLE_FILE"), "optional PEM bundle trusted for the Registry")
	flag.Parse()

	if strings.TrimSpace(*output) == "" {
		fatal(errors.New("-output is required"))
	}
	bundle, err := appcatalog.BuildPortableBundle(strings.TrimSpace(*source))
	if err != nil {
		fatal(fmt.Errorf("build catalog release bundle: %w", err))
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
		fatal(fmt.Errorf("create bundle output directory: %w", err))
	}
	digest, err := oci.WriteArtifactArchive(*output, oci.Artifact{
		ArtifactType: bundle.ArtifactType,
		Blobs:        []oci.ArtifactBlob{{MediaType: bundle.LayerMediaType, Data: bundle.SourceLayer}},
		Annotations:  bundle.Annotations,
	})
	if err != nil {
		fatal(fmt.Errorf("write catalog release OCI archive: %w", err))
	}

	if strings.TrimSpace(*reference) == "" {
		fmt.Println(digest)
		return
	}
	immutable, authority, err := immutableReference(*reference, digest)
	if err != nil {
		fatal(err)
	}
	if strings.TrimSpace(*endpoint) != "" {
		authority = strings.TrimSpace(*endpoint)
	}
	client, err := oci.NewClient(oci.ClientOptions{
		Endpoint: authority,
		Credentials: oci.Credentials{
			Username: os.Getenv("BREAKFIX_REGISTRY_USERNAME"),
			Password: os.Getenv("BREAKFIX_REGISTRY_PASSWORD"),
		},
		TrustBundleFile: strings.TrimSpace(*trustBundle),
	})
	if err != nil {
		fatal(fmt.Errorf("create Registry client: %w", err))
	}
	if err := client.PushOCIArchive(context.Background(), strings.TrimSpace(*reference), *output); err != nil {
		fatal(fmt.Errorf("push catalog release OCI artifact: %w", err))
	}
	fmt.Println(immutable)
}

func immutableReference(reference, digest string) (string, string, error) {
	reference = strings.TrimSpace(reference)
	if strings.Contains(reference, "@") {
		return "", "", errors.New("catalog release publish reference must use a mutable tag, not a digest")
	}
	authority, repository, found := strings.Cut(reference, "/")
	if !found || strings.TrimSpace(authority) == "" || strings.TrimSpace(repository) == "" {
		return "", "", fmt.Errorf("catalog release reference %q must include Registry authority and repository", reference)
	}
	if lastColon := strings.LastIndex(repository, ":"); lastColon >= 0 {
		if strings.TrimSpace(repository[lastColon+1:]) == "" {
			return "", "", fmt.Errorf("catalog release reference %q has an empty tag", reference)
		}
		repository = repository[:lastColon]
	}
	if strings.TrimSpace(repository) == "" {
		return "", "", fmt.Errorf("catalog release reference %q has an empty repository", reference)
	}
	return authority + "/" + repository + "@" + digest, authority, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "catalog release:", err)
	os.Exit(1)
}
