package catalog

import (
	"fmt"

	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
)

const (
	ReleaseArtifactType    = "application/vnd.breakfix.catalog.release.v1"
	ReleaseSourceLayerType = "application/vnd.breakfix.catalog.source.v1.tar+gzip"
)

// PortableBundle is the source payload passed to an OCI artifact writer. The
// application package intentionally does not depend on the OCI adapter.
type PortableBundle struct {
	ArtifactType   string
	LayerMediaType string
	SourceLayer    []byte
	Annotations    map[string]string
	Manifest       catalogdomain.SourceManifest
}

// BuildPortableBundle produces the deterministic source layer that an OCI
// adapter can package and publish. It performs no Registry operation.
func BuildPortableBundle(root string) (*PortableBundle, error) {
	source, err := LoadPortableSource(root)
	if err != nil {
		return nil, err
	}
	files, err := readSourceFiles(source.Root)
	if err != nil {
		return nil, fmt.Errorf("read portable source bundle: %w", err)
	}
	layer, err := writeSourceLayer(files)
	if err != nil {
		return nil, err
	}
	return &PortableBundle{
		ArtifactType:   ReleaseArtifactType,
		LayerMediaType: ReleaseSourceLayerType,
		SourceLayer:    layer,
		Annotations: map[string]string{
			"org.opencontainers.image.title":   source.Manifest.Metadata.Name,
			"org.opencontainers.image.version": source.Manifest.Metadata.Version,
		},
		Manifest: source.Manifest,
	}, nil
}
