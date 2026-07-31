//go:build !clionly

package web

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// TestFS_Resolves is the regression test for #1567: a fresh clone or
// `git worktree add` used to fail `go build ./...` outright with
// "pattern all:out: no matching files found", because web/out/ is a
// gitignored build artifact and nothing tracked lived in it. If this
// package compiles at all the embed resolved — but assert the contents
// too, so a future "tidy up web/out" commit trips a named test instead
// of the cryptic embed error.
func TestFS_Resolves(t *testing.T) {
	f, err := FS()
	if err != nil {
		t.Fatalf("FS() error = %v", err)
	}

	if IsBuilt(f) {
		// A real export is staged (release/CI binary job). Nothing more
		// to check here — the placeholder is deliberately absent then.
		return
	}

	// Degraded tree: the tracked placeholder must be present AND must
	// explain itself. A silent stub is the failure mode this whole
	// change exists to remove.
	body, err := fs.ReadFile(f, PlaceholderFile)
	if err != nil {
		t.Fatalf("no index.html and no %s in the embedded FS: %v\n"+
			"web/out/%s is tracked in git and must not be deleted (#1567)",
			PlaceholderFile, err, PlaceholderFile)
	}
	for _, want := range []string{"web UI was not built", "pnpm build"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("placeholder page does not mention %q — it is the only thing telling a user how to fix a UI-less binary", want)
		}
	}
}

func TestIsBuilt(t *testing.T) {
	tests := []struct {
		name string
		fsys fs.FS
		want bool
	}{
		{"nil", nil, false},
		{"placeholder only", fstest.MapFS{PlaceholderFile: {Data: []byte("x")}}, false},
		{"empty", fstest.MapFS{}, false},
		{"real export", fstest.MapFS{
			"index.html":          {Data: []byte("<html>")},
			"_next/static/a.js":   {Data: []byte("x")},
			"login.html":          {Data: []byte("<html>")},
			"_next/static/a.css":  {Data: []byte("x")},
			"favicon.ico":         {Data: []byte("x")},
			"_next/static/b.js":   {Data: []byte("x")},
			"crews/agents.html":   {Data: []byte("<html>")},
			"_next/static/c.js":   {Data: []byte("x")},
			"_next/static/d.js":   {Data: []byte("x")},
			"_next/static/e.json": {Data: []byte("{}")},
		}, true},
		// The shape the old hand-rolled workaround produced. IsBuilt
		// can't tell it apart from a real export by design — that is
		// what scripts/embed-web-out.sh's `verify` (which also requires
		// _next/) is for on the release path.
		{"hand-rolled stub", fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBuilt(tt.fsys); got != tt.want {
				t.Errorf("IsBuilt() = %v, want %v", got, tt.want)
			}
		})
	}
}
