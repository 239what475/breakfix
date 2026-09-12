// Package documentpractice owns the future documentation-practice product.
// It intentionally does not import Scenario or Catalog: documentation pages
// are sourced, versioned, and published independently from operations cases.
package documentpractice

import (
	"fmt"
	"strings"
)

// APIPrefix reserves the public namespace for document sources, pages, and
// anchored examples. No route is exposed until the Kubernetes documentation
// synchronization and reader flow have a usable end-to-end implementation.
const APIPrefix = "/api/documentation"

// PageLocation identifies the upstream position to which one or more
// executable documentation examples will attach.
type PageLocation struct {
	Source   string
	Revision string
	Path     string
	Anchor   string
}

func (p PageLocation) Validate() error {
	if strings.TrimSpace(p.Source) == "" || strings.TrimSpace(p.Revision) == "" || strings.TrimSpace(p.Path) == "" {
		return fmt.Errorf("documentation page location requires source, revision, and path")
	}
	if strings.HasPrefix(p.Path, "/") || strings.Contains(p.Path, "..") {
		return fmt.Errorf("documentation page path must be a relative source path")
	}
	return nil
}
