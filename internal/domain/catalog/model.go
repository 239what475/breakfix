// Package catalog defines portable release identities. Runtime installation
// state is added by the catalog application; this package never models a
// GenerationWorkflow or a second Catalog read model.
package catalog

import (
	"errors"
	"regexp"
	"strings"
)

var contentRevisionPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ContentRevision identifies canonical portable source bytes. It is distinct
// from an OCI manifest digest and from a runtime artifact identity.
type ContentRevision string

func (r ContentRevision) Valid() bool { return contentRevisionPattern.MatchString(string(r)) }

func (r ContentRevision) Validate() error {
	if !r.Valid() {
		return errors.New("content revision must be a lowercase sha256 digest")
	}
	return nil
}

// BundleDigest identifies the immutable OCI release selected by deployment
// configuration. It deliberately has a separate type from source content.
type BundleDigest string

func (d BundleDigest) Valid() bool { return contentRevisionPattern.MatchString(string(d)) }

// BundleDigestFromReference extracts the immutable OCI manifest digest from a
// configured Catalog Release reference. The repository authority is deployment
// configuration; the durable release identity is only its immutable digest.
func BundleDigestFromReference(reference string) (BundleDigest, error) {
	_, value, found := strings.Cut(strings.TrimSpace(reference), "@")
	digest := BundleDigest(value)
	if !found || !digest.Valid() {
		return "", errors.New("catalog release must use an immutable OCI digest reference")
	}
	return digest, nil
}
