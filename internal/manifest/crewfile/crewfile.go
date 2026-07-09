// Package crewfile holds the shared crew-file delivery primitives used by
// BOTH manifest apply paths: the legacy combined-manifest planner
// (internal/manifest) and the SPEC-2 standalone kind:Crew planner
// (internal/manifest/kinds). Living in a leaf package (neither imports the
// other) is what lets the two paths deliver `files:` byte-for-byte
// identically instead of drifting — the #921 failure mode, where the kinds
// path had no Files support at all and silently dropped the block.
package crewfile

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxBytes caps one delivered crew file at 1 MiB — same budget as an inline
// code-step body and the script step's stdout cap. Bigger assets belong in
// object storage or the devcontainer image, not the manifest flow.
const MaxBytes = 1 << 20

// SharedPrefix is the only in-crew tree a manifest file may target. It maps
// to /crew/shared inside the container — the same root the script step's path
// fence anchors to (internal/pipeline/runner_script.go).
const SharedPrefix = "shared/"

// File is one local file to deliver into the crew's shared volume.
type File struct {
	// Src is the local path, relative to the manifest file's directory
	// (absolute paths allowed).
	Src string `yaml:"src" json:"src"`
	// Dest is the in-crew path under shared/ (e.g. "shared/scripts/parse.py").
	// "/crew/"-prefixed forms are normalized. Empty = shared/<basename(src)>.
	Dest string `yaml:"dest,omitempty" json:"dest,omitempty"`
}

// NormalizeDest resolves a File's destination to the canonical "shared/..."
// form: defaults to shared/<basename(src)>, accepts "/crew/shared/..."
// spellings, and rejects traversal or any path outside the shared tree (the
// crew's /output, /secrets, agent homes are off-limits to declarative
// delivery on purpose).
func NormalizeDest(src, dest string) (string, error) {
	d := strings.TrimSpace(dest)
	if d == "" {
		s := strings.TrimSpace(src)
		if s == "" {
			return "", fmt.Errorf("src is required")
		}
		d = SharedPrefix + path.Base(filepath.ToSlash(s))
	}
	d = strings.TrimPrefix(filepath.ToSlash(d), "/crew/")
	d = strings.TrimPrefix(d, "/")
	clean := path.Clean(d)
	if !strings.HasPrefix(clean, SharedPrefix) || clean == SharedPrefix {
		return "", fmt.Errorf("dest %q must be a file under %s (no traversal; e.g. shared/scripts/parse.py)", dest, SharedPrefix)
	}
	return clean, nil
}

// Load reads a File's source from disk, resolving a relative Src against
// baseDir (the manifest file's directory), and enforces the size cap.
func Load(baseDir, src string) ([]byte, error) {
	s := strings.TrimSpace(src)
	if s == "" {
		return nil, fmt.Errorf("src is required")
	}
	p := s
	if !filepath.IsAbs(p) {
		p = filepath.Join(baseDir, p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("src %q: %w", src, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("src %q is a directory — list files individually", src)
	}
	if info.Size() > MaxBytes {
		return nil, fmt.Errorf("src %q is %d bytes, exceeds the %d-byte crew-file cap — ship big assets via the devcontainer image or object storage", src, info.Size(), MaxBytes)
	}
	return os.ReadFile(p)
}
