// Package safepath centralises path-component validation so other
// packages can join user-supplied identifiers (crew IDs, slugs,
// workspace IDs, etc.) into filesystem paths without risking traversal
// out of the intended root.
//
// Callers wanting "is this whole path inside that root?" should use
// EnsureInside; callers wanting "is this single token safe as a path
// component?" should use ValidateComponent.
package safepath

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsafe is returned by every helper in this package when input would
// allow path traversal or other filesystem trickery. Callers may
// errors.Is against this to map the failure to an HTTP 400 / refuse
// without leaking the underlying string back into a log.
var ErrUnsafe = errors.New("safepath: unsafe path")

// ValidateComponent rejects empty strings, "." / "..", anything with a
// path separator (forward or back slash, even on Linux — Windows shares
// reach Linux containers and so do uploaded archives), null bytes, and
// any value that filepath.IsLocal considers escape-y. Returns the input
// unchanged on success so call sites can read
//
//	id, err := safepath.ValidateComponent(team.ID)
//
// without a temporary.
func ValidateComponent(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("%w: empty component", ErrUnsafe)
	}
	if s == "." || s == ".." {
		return "", fmt.Errorf("%w: reserved component %q", ErrUnsafe, s)
	}
	if strings.ContainsAny(s, "/\\\x00") {
		return "", fmt.Errorf("%w: contains separator or NUL", ErrUnsafe)
	}
	if !filepath.IsLocal(s) {
		return "", fmt.Errorf("%w: not a local path", ErrUnsafe)
	}
	return s, nil
}

// JoinUnder joins components onto base after running each through
// ValidateComponent. Returns ErrUnsafe if any component fails, or if
// the resulting path is not a descendant of base after filepath.Clean
// (defence in depth — IsLocal already rejects ".." but a future caller
// might add a base join with a relative root).
func JoinUnder(base string, components ...string) (string, error) {
	for _, c := range components {
		if _, err := ValidateComponent(c); err != nil {
			return "", err
		}
	}
	joined := filepath.Join(append([]string{base}, components...)...)
	cleanBase := filepath.Clean(base)
	if !strings.HasPrefix(joined, cleanBase+string(filepath.Separator)) && joined != cleanBase {
		return "", fmt.Errorf("%w: %q escapes base %q", ErrUnsafe, joined, cleanBase)
	}
	return joined, nil
}

// EnsureInside verifies that target resolves to a path within base. Use
// after building a path from untrusted segments (e.g. tar entries) to
// double-check before any filesystem write. Both inputs are cleaned;
// equality with base is allowed (writing to base itself is fine).
func EnsureInside(base, target string) error {
	cleanBase := filepath.Clean(base)
	cleanTarget := filepath.Clean(target)
	if cleanTarget == cleanBase {
		return nil
	}
	rel, err := filepath.Rel(cleanBase, cleanTarget)
	if err != nil {
		return fmt.Errorf("%w: cannot relativise %q against %q: %v", ErrUnsafe, target, base, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %q is outside %q", ErrUnsafe, target, base)
	}
	return nil
}

// CleanAbs returns filepath.Clean(p) when p is already absolute,
// otherwise resolves it against base. Result is rejected if it escapes
// base (so a relative input like "../../etc/passwd" can't sneak past).
// Use for CLI inputs that may be absolute or relative — e.g.
// `crewship backup --output ~/backups` vs `--output ./backups`.
func CleanAbs(base, p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafe)
	}
	if strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("%w: path contains NUL", ErrUnsafe)
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	resolved := filepath.Join(base, p)
	if err := EnsureInside(base, resolved); err != nil {
		return "", err
	}
	return resolved, nil
}
