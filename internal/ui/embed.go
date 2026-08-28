// Package ui embeds the compiled web application.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded application root. It returns nil until the Vite
// build has produced index.html, allowing Go-only development and tests.
func FS() fs.FS {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return nil
	}
	return root
}
