// Package safepath is the one place in this repo that answers "is this
// path safe?". It turns untrusted input — crew IDs, slugs, workspace
// IDs, agent-supplied memory paths, tar entries, CLI arguments — into a
// filesystem path that provably cannot escape the root it was meant to
// stay under.
//
// There is deliberately no second package for this. internal/pathsafe
// used to answer the same question for whole relative paths, and two
// plausible answers is how call sites end up on the weaker one without
// anybody noticing: that is the shape of the traversal bugs fixed in
// #1569 and #1581/#1582. pathsafe.Join now lives here as JoinRel.
//
// Pick by what you hold:
//
//	one untrusted token (id, slug, filename)  → ValidateComponent
//	root + N untrusted tokens                 → JoinUnder
//	root + one untrusted relative subpath     → JoinRel
//	a path you already built, re-check it     → EnsureInside
//	a CLI arg that may be absolute or relative→ CleanAbs
//
// Every helper fails closed and returns an error wrapping ErrUnsafe.
// Segment validation is shared: JoinUnder and JoinRel both run each
// component through ValidateComponent, so the package has exactly one
// notion of a safe path component.
//
// SECURITY NOTE — this package is *lexical*. It does not touch the
// filesystem, so it cannot see a symlink. It closes the textual
// traversal hole only. Any caller that writes into a directory another
// uid can also write to must add the filesystem layers on top, in the
// shape the recent security fixes established:
//
//   - lexical confinement here (JoinRel / JoinUnder / EnsureInside),
//   - filepath.EvalSymlinks containment as the DIAGNOSIS (it produces
//     the error the client sees, and it is blind to a leaf that does not
//     exist yet),
//   - an *os.Root as the ENFORCEMENT (one openat per component, refuses
//     any symlink leaving the root — this is what closes the create
//     path).
//
// See internal/sidecar/memory_write.go safeJoinUnder and
// internal/memory/tools.go assertMemoryFile for the composed use.
//
// If a call site needs something this package does not do, that is a gap
// to fill here — not a reason to grow a second helper elsewhere.
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

// JoinRel confines a single untrusted *relative subpath* under base and
// returns the cleaned, confined absolute-or-base-relative result. Use it
// where the untrusted value is a whole path ("daily/2026-07-09.md")
// rather than a list of tokens; use JoinUnder when you hold the tokens
// separately, because that form makes the segment boundaries explicit.
//
// Rejected, all with an error wrapping ErrUnsafe:
//
//   - empty base or empty rel
//   - rel containing a NUL byte (C-string truncation defence against a
//     downstream Linux syscall)
//   - absolute rel
//   - any segment of the cleaned rel that ValidateComponent refuses —
//     which covers "..", "." smuggled past Clean, embedded separators
//     (forward AND back slash, on every OS), and non-local components
//   - a joined path that is not base or a descendant of base
//
// rel is Cleaned first, so "./AGENT.md" and "daily/./x.md" are accepted
// and normalised; "daily/../../etc/passwd" cleans to "../etc/passwd"
// and is refused on its first segment.
//
// A rel of "." returns base itself, which is intentional and matches
// EnsureInside (base is inside base) and JoinUnder with no components. A
// caller that needs a *file* must reject that case itself.
func JoinRel(base, rel string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("%w: empty base", ErrUnsafe)
	}
	if rel == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafe)
	}
	if strings.ContainsRune(rel, '\x00') {
		return "", fmt.Errorf("%w: path contains NUL", ErrUnsafe)
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: absolute path", ErrUnsafe)
	}

	cleanBase := filepath.Clean(base)
	cleanRel := filepath.Clean(rel)
	if cleanRel == "." {
		return cleanBase, nil
	}
	// Validate the cleaned relative value segment by segment. Checking the
	// tainted input itself (rather than only the join) is what makes the
	// traversal provably rejected before it can influence the join.
	for _, seg := range strings.Split(cleanRel, string(filepath.Separator)) {
		if _, err := ValidateComponent(seg); err != nil {
			return "", err
		}
	}

	// Belt-and-suspenders: confirm the join really landed under base.
	joined := filepath.Join(cleanBase, cleanRel)
	if err := EnsureInside(cleanBase, joined); err != nil {
		return "", err
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
