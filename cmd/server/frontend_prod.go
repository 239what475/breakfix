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
