package server

import (
	"fmt"

	docsource "github.com/breakfix/breakfix/internal/adapter/documentation"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
)

// openDocumentationLibrary verifies the pinned library against the deployment
// identity. The library is the reader surface: pages, the outline tree, and
// the pinned identity that scopes each reader's blank practice scenario.
func openDocumentationLibrary(cfg config.Config) (*docsource.Library, error) {
	if !cfg.Documentation.Enabled() {
		return nil, nil
	}
	identity := docsource.LibraryIdentity{
		SourceID: cfg.Documentation.SourceID, Repository: cfg.Documentation.Repository,
		Commit: cfg.Documentation.Revision, Version: cfg.Documentation.Version, Language: cfg.Documentation.Language,
		License: cfg.Documentation.License,
	}
	library, err := docsource.NewPinnedLibrary(identity, cfg.Documentation.LibraryRoot)
	if err != nil {
		return nil, fmt.Errorf("load pinned documentation library: %w", err)
	}
	return &library, nil
}
