// Package web embeds the built management-plane UI so it ships inside the Go
// binary. The real bundle under dist/ is produced by `npm run build` and is
// gitignored; a committed dist/.gitkeep lets `//go:embed all:dist` compile in a
// fresh checkout (the `all:` prefix is required to match the dotfile).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the built UI assets rooted at the dist directory. Before the
// UI is built it contains only the placeholder, so serving 404s until assets
// exist — the binary still compiles and tests still run.
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
