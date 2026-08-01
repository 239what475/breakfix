//go:build !dev

package ui

import (
	"embed"
	"io/fs"
)

// assets is populated from web/dist by the web-assets build target.
//
//go:embed all:assets
var embeddedAssets embed.FS

func Filesystem() (fs.FS, error) {
	return fs.Sub(embeddedAssets, "assets")
}
