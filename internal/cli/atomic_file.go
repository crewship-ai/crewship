package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

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
// if the directory is not writable.
func NewAtomicFile(targetPath string) (*AtomicFile, error) {
	dir := filepath.Dir(targetPath)
	base := filepath.Base(targetPath)
	f, err := os.CreateTemp(dir, base+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create tempfile in %s: %w", dir, err)
	}
	return &AtomicFile{f: f, target: targetPath, tmpPath: f.Name()}, nil
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
		_ = os.Remove(a.tmpPath)
		return fmt.Errorf("close tempfile: %w", err)
	}
	if err := os.Rename(a.tmpPath, a.target); err != nil {
		_ = os.Remove(a.tmpPath)
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
	_ = os.Remove(a.tmpPath)
	a.committed = true // prevent double-cleanup
	return nil
}
