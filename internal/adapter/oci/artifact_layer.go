package oci

import (
	"errors"
	"fmt"
	"strings"
)

// ArtifactLayerReader exposes one explicitly typed OCI artifact layer to an
// application use case. It keeps OCI manifest parsing in the adapter while
// leaving catalog source semantics to the catalog application package.
type ArtifactLayerReader struct {
	ArtifactType string
	LayerType    string
}

func (r ArtifactLayerReader) ReadSourceLayer(archivePath string) ([]byte, error) {
	if strings.TrimSpace(r.ArtifactType) == "" || strings.TrimSpace(r.LayerType) == "" {
		return nil, errors.New("OCI artifact layer reader requires artifact and layer types")
	}
	manifest, err := ReadArtifactArchive(archivePath)
	if err != nil {
		return nil, err
	}
	if manifest.ArtifactType != r.ArtifactType || len(manifest.Blobs) != 1 || manifest.Blobs[0].MediaType != r.LayerType {
		return nil, fmt.Errorf("OCI artifact does not contain one expected %s layer", r.LayerType)
	}
	return ReadArtifactBlob(archivePath, manifest.Blobs[0].Digest)
}
