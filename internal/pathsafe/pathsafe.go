// Package pathsafe is the single chokepoint for turning an
// agent-controlled relative path into a concrete filesystem path
// confined under a trusted root. It exists so the memory subsystem and
// the sidecar's memory HTTP surface validate traversal identically
// instead of each re-deriving a Clean+prefix check that can drift.
//
// It is the memory-tree analogue of internal/api.normalizeRequestPath:
// same rejection philosophy (no absolute, no "..", no NUL), extended to
// also perform the confinement join so callers get back a path they can
// hand straight to a filesystem syscall.
//
// SECURITY NOTE: Join is a *lexical* guard. It does not resolve
// symlinks, so callers that write into a directory an untrusted uid can
// also write to must still lstat the final component (and, where the
// threat model includes planted parent-directory symlinks, EvalSymlinks
// the parent) to defeat a confused-deputy symlink swap. Join closes the
// textual-traversal hole; the symlink guards close the TOCTOU hole. Both
// layers are required — see internal/memory/tools.go assertMemoryFile
// and internal/sidecar/memory_write.go safeJoinUnder for the composed
// use.
package pathsafe

import (
	"errors"
	"path/filepath"
	"strings"
)

// ErrUnsafePath is returned by Join when rel is empty/absolute/contains
// a NUL byte, when rel traverses above root via "..", or when the
// cleaned join would land outside root. Callers surface it as a 400/403
// "illegal file path" without echoing the offending value back.
var ErrUnsafePath = errors.New("pathsafe: unsafe path")

// Join confines an agent-controlled relative path `rel` under the
// trusted directory `root` and returns the cleaned, confined path.
//
// Rejection rules, applied to `rel` (the tainted value) BEFORE the join
// and to the join AFTER Clean:
//
//   - empty root or empty rel
//   - rel containing a NUL byte (C-string truncation defense against a
//     downstream Linux syscall)
//   - absolute rel (filepath.IsAbs)
//   - rel whose cleaned form is ".." or begins with "../" or contains a
//     "/../" segment anywhere — an explicit guard on the raw input so
//     the traversal is rejected before any join takes place
//   - a joined+cleaned path that is neither root itself nor a descendant
//     of root (separator-anchored prefix, so root="/a/b" does not admit
//     "/a/bevil")
//
// On success it returns filepath.Clean(filepath.Join(root, rel)), which
// is guaranteed to be root or a path under root.
func Join(root, rel string) (string, error) {
	if root == "" || rel == "" {
		return "", ErrUnsafePath
	}
	if strings.ContainsRune(rel, 0) {
		return "", ErrUnsafePath
	}
	if filepath.IsAbs(rel) {
		return "", ErrUnsafePath
	}

	// Guard the raw relative input for "..": deliberately checked on the
	// tainted value itself so the traversal is provably rejected before
	// it can influence the join.
	cleanRel := filepath.Clean(rel)
	sep := string(filepath.Separator)
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+sep) || strings.Contains(cleanRel, sep+".."+sep) {
		return "", ErrUnsafePath
	}

	// Belt-and-suspenders confinement: Clean the join and verify it
	// stays under Clean(root). Catches any residual escape the raw guard
	// above did not (e.g. odd multi-".." collapses on some platforms).
	cleanRoot := filepath.Clean(root)
	joined := filepath.Clean(filepath.Join(cleanRoot, cleanRel))
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+sep) {
		return "", ErrUnsafePath
	}
	return joined, nil
}

// Under reports whether `path` (already absolute or cleaned by the
// caller) sits at or under `root`, using the same separator-anchored
// prefix rule as Join. It is the containment predicate used by callers
// that build a path via other means (e.g. after EvalSymlinks) and only
// need the yes/no check.
func Under(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator))
}
