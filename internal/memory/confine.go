package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/safepath"
)

// Confinement primitives shared by every door into a memory tree.
//
// Lexical confinement (internal/safepath) answers "does this path text
// stay under the root". It is necessary and it is not sufficient: a
// symlink planted inside the tree keeps the text unchanged while
// redirecting the bytes. The agent owns its own .memory directory, so
// planting one is within its reach — which makes the canonicalised
// check below part of the guarantee, not a belt on top of it.
//
// The dispatcher enforced this as a method on itself (assertMemoryFile /
// isInsideMemoryRoot, tools.go) and the sidecar has its own final-
// component guard. This is the same logic in the form a caller with one
// root can reuse; the dispatcher delegates here so the two cannot drift.

// AssertInsideRoot verifies that path is confined to root once symlinks
// are resolved.
//
// A non-existent leaf is accepted — the caller is usually about to
// create it, and refusing would break every first write. What is
// rejected is a leaf that already exists AS a symlink, and any path
// whose canonicalised parent falls outside root.
//
// The parent must exist. That is deliberate: resolving it is the whole
// check, so an unresolvable parent is a fail-closed signal rather than a
// reason to skip verification. Callers creating new directories should
// run EnsureDirNoFollow first.
func AssertInsideRoot(root, path string) error {
	if root == "" {
		return fmt.Errorf("memory confinement: empty root")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlinked memory entry: %s", filepath.Base(path))
		}
	}
	canonParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("canonicalise parent: %w", err)
	}
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("canonicalise memory root: %w", err)
	}
	if !isUnder(canonRoot, filepath.Join(canonParent, filepath.Base(path))) {
		return fmt.Errorf("path escapes memory root: %s", filepath.Base(path))
	}
	return nil
}

// isUnder reports whether canon is root or a descendant of it. Both
// arguments must already be canonical (EvalSymlinks'd), or Rel compares
// a traversed path against an untraversed one and can say yes to an
// escape.
func isUnder(canonRoot, canon string) bool {
	rel, err := filepath.Rel(canonRoot, canon)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// EnsureDirNoFollow creates dir under root, one segment at a time,
// refusing any existing segment that is a symlink.
//
// os.MkdirAll is not usable here: it succeeds silently when an
// intermediate component is a symlink pointing out of the tree, which
// is exactly the redirection this package has to stop. Creating the
// chain segment by segment is what makes "the directory I created is
// the directory I will write into" true.
func EnsureDirNoFollow(root, dir string) error {
	if root == "" {
		return fmt.Errorf("memory confinement: empty root")
	}
	canonRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("canonicalise memory root: %w", err)
	}
	// The relative segment list is derived from the root AS GIVEN, not
	// from its canonical form: callers build dir by joining onto the
	// same root string, and on a host where the root itself sits under
	// a symlinked prefix (/var → /private/var on macOS) comparing the
	// two forms would read every legitimate path as an escape. The walk
	// below then starts from the canonical root, so the segments are
	// created and checked in the resolved tree.
	cleanDir := filepath.Clean(dir)
	rel, err := filepath.Rel(filepath.Clean(root), cleanDir)
	if err != nil {
		return fmt.Errorf("memory confinement: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("directory escapes memory root: %s", filepath.Base(cleanDir))
	}
	if rel == "." {
		return nil
	}

	cur := canonRoot
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		// Validate every segment before it is joined. Rel already
		// guarantees ".." can only appear at the front (rejected above),
		// so this is defence in depth — but it puts the check on the
		// value that actually reaches the syscall rather than on a
		// property of the string three lines up.
		if _, err := safepath.ValidateComponent(seg); err != nil {
			return fmt.Errorf("memory directory component: %w", err)
		}
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("refusing symlinked memory directory: %s", seg)
		case err == nil && !info.IsDir():
			return fmt.Errorf("memory path component is not a directory: %s", seg)
		case err == nil:
			continue
		case os.IsNotExist(err):
			if mkErr := os.Mkdir(cur, 0o755); mkErr != nil && !os.IsExist(mkErr) {
				return fmt.Errorf("create memory directory %s: %w", seg, mkErr)
			}
		default:
			return fmt.Errorf("stat memory directory %s: %w", seg, err)
		}
	}
	return nil
}

// ReadFileNoFollow reads a regular file without following a symlink at
// the final component. It is the exported form of the reader the FTS
// indexer already uses (#1043); every new read door into a memory tree
// should go through it rather than os.ReadFile.
func ReadFileNoFollow(path string) ([]byte, error) {
	return readRegularNoFollow(path)
}
