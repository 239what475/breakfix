//go:build !dev

package main

import (
	"embed"
	"io/fs"
	"log/slog"
)

//go:embed frontend/dist
var embeddedFS embed.FS

var frontendFS fs.FS

func init() {
	var err error
	frontendFS, err = fs.Sub(embeddedFS, "frontend/dist")
	if err != nil {
		slog.Error("failed to mount embedded frontend", "err", err)
	}
}

// spaFS wraps fs.FS to fall back to index.html for SPA routing.
type spaFS struct{ fs.FS }

func (s *spaFS) Open(name string) (fs.File, error) {
	f, err := s.FS.Open(name)
	if err != nil {
		return s.FS.Open("index.html")
	}
	return f, nil
}
