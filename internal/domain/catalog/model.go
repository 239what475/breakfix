// Package catalog defines portable release identities. Runtime installation
// state is added by the catalog application; this package never models a
// GenerationWorkflow or a second Roadmap read model.
package catalog

import (
	"errors"
	"regexp"
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
