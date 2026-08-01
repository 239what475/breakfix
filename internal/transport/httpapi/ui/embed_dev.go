//go:build dev

package ui

import "io/fs"

// Filesystem is disabled for local development, where Vite serves the web UI.
func Filesystem() (fs.FS, error) {
	return nil, nil
}
