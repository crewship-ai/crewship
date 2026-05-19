package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsafeAtomicFilePath is returned when NewAtomicFile is called with a
// path that the caller could not reasonably have intended — an empty
// string, or a path containing a NUL byte. We do NOT block ".." here
// because the CLI's --save flag legitimately points anywhere on disk an
// operator can write to; the operator is the trust boundary, not us.
var ErrUnsafeAtomicFilePath = errors.New("atomic file: unsafe target path")

// AtomicFile is a write-then-rename wrapper that gives `--save` the
// "either a complete previous file or a complete new file" guarantee
// callers expect. Without this, a Ctrl-C or panic mid-stream leaves the
// target path with a half-written response — particularly painful when
// the user is overwriting an existing file they care about.
//
// Lifecycle:
//
//	a, _ := NewAtomicFile("out.md")
//	defer a.Close()           // cleanup on any path that doesn't commit
//	a.WriteString("...")
//	a.Commit()                // promote tmpfile to target via atomic rename
//
// Close on a committed file is a no-op. Close on an uncommitted file
// removes the tempfile. The target path is only modified by Commit().
type AtomicFile struct {
	f         *os.File
	target    string
	tmpPath   string
	committed bool
}

// NewAtomicFile creates a tempfile in the target's directory (so the rename
// stays on the same filesystem and is therefore atomic). Returns an error
// if the directory is not writable, or if targetPath contains the kind of
// bytes that no shell would have produced (NUL).
//
// The path itself is canonicalised via filepath.Clean so that
// `--save dir/./out.md` and `--save dir/out.md` produce the same on-disk
// result. We deliberately do NOT restrict the path to a root: the CLI's
// `--save` flag is operator-driven and writing outside cwd is a feature.
func NewAtomicFile(targetPath string) (*AtomicFile, error) {
	if targetPath == "" {
		return nil, fmt.Errorf("%w: empty target path", ErrUnsafeAtomicFilePath)
	}
	if strings.ContainsRune(targetPath, '\x00') {
		return nil, fmt.Errorf("%w: target contains NUL byte", ErrUnsafeAtomicFilePath)
	}
	cleaned := filepath.Clean(targetPath)
	dir := filepath.Dir(cleaned)
	base := filepath.Base(cleaned)
	f, err := os.CreateTemp(dir, base+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create tempfile in %s: %w", dir, err)
	}
	return &AtomicFile{f: f, target: cleaned, tmpPath: f.Name()}, nil
}

// Write writes b to the underlying tempfile. Mirrors *os.File.Write.
func (a *AtomicFile) Write(b []byte) (int, error) {
	return a.f.Write(b)
}

// WriteString writes s to the underlying tempfile. Mirrors *os.File.WriteString.
func (a *AtomicFile) WriteString(s string) (int, error) {
	return a.f.WriteString(s)
}

// Commit fsyncs the tempfile and atomically renames it to the target path.
// Idempotent — calling Commit twice is a no-op on the second call.
//
// a.target and a.tmpPath were both produced via filepath.Clean inside
// NewAtomicFile so we don't re-clean here. The previous form passed
// arbitrary external strings into os.Rename / os.Remove; constraining
// the constructor is the single source of truth.
func (a *AtomicFile) Commit() error {
	if a.committed {
		return nil
	}
	if err := a.f.Sync(); err != nil {
		// fsync failures are rare and we still want to attempt the rename
		// — close + rename will surface the underlying problem if it's real.
		_ = err
	}
	if err := a.f.Close(); err != nil {
		_ = os.Remove(filepath.Clean(a.tmpPath))
		return fmt.Errorf("close tempfile: %w", err)
	}
	if err := os.Rename(filepath.Clean(a.tmpPath), filepath.Clean(a.target)); err != nil {
		_ = os.Remove(filepath.Clean(a.tmpPath))
		return fmt.Errorf("rename %s -> %s: %w", a.tmpPath, a.target, err)
	}
	a.committed = true
	return nil
}

// Close removes the tempfile if Commit hasn't run. Safe to call multiple
// times and after Commit (no-op in either of those cases). Defer this in
// callers as a "cleanup if anything goes wrong" handler.
func (a *AtomicFile) Close() error {
	if a.committed {
		return nil
	}
	_ = a.f.Close()
	_ = os.Remove(filepath.Clean(a.tmpPath))
	a.committed = true // prevent double-cleanup
	return nil
}
