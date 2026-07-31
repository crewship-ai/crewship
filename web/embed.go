//go:build !clionly

package web

import (
	"embed"
	"io/fs"
)

// PlaceholderFile is the tracked stub inside web/out/ that keeps the
// directory non-empty so the embed directive below resolves in a fresh
// clone or `git worktree add`. See web/out/.placeholder.html and #1567.
//
// The name is deliberately not index.html: the real Next.js export owns
// that path, and a same-named placeholder would show as a modified
// tracked file after every `make build`.
const PlaceholderFile = ".placeholder.html"

//go:embed all:out
var embedded embed.FS

func FS() (fs.FS, error) {
	return fs.Sub(embedded, "out")
}

// IsBuilt reports whether the embedded filesystem holds a real Next.js
// static export rather than only the tracked placeholder.
//
// The export always writes an index.html at the root; the placeholder
// deliberately never does. Callers use this to refuse to pretend the UI
// works — internal/api.StaticFileHandler answers 503 with the placeholder
// page, and `crewship start` logs a warning at boot. Without that, a
// binary compiled over an unbuilt web/out/ serves a blank 200 and looks
// like a frontend bug rather than a missing build step.
func IsBuilt(f fs.FS) bool {
	if f == nil {
		return false
	}
	_, err := fs.Stat(f, "index.html")
	return err == nil
}
