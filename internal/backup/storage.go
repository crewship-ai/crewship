package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrUnsafeBackupPath is returned when a caller passes a path that no
// `crewship backup` CLI invocation would have produced — empty, or
// containing a NUL byte. Restoring/inspecting an arbitrary path on the
// operator's box is a feature (admins legitimately point at network
// mounts and tarballs in /tmp), so we do NOT restrict to a root, but we
// also refuse the kind of paths that can only come from malicious
// callers wiring in untrusted strings.
var ErrUnsafeBackupPath = errors.New("backup storage: unsafe path")

// ErrBundleNotFound is returned by Exists when the bundle file is
// absent from the storage backend. Used by API/CLI handlers to map
// to HTTP 404 without bypassing the storage provider abstraction
// (which the previous os.Stat in internal/api did). Wrap distinct
// from os.ErrNotExist so callers can branch via errors.Is without
// importing os.
var ErrBundleNotFound = errors.New("backup: bundle not found")

// Exists reports whether a bundle file is present at the supplied
// path. Uses the active storage provider (LocalStorageOps by default)
// so filesystem access stays inside the backup package — API handlers
// MUST NOT call os.Stat directly per the provider-pattern guideline
// (internal/**/*.go: never access filesystem directly).
//
// Returns:
//   - (true,  nil) if the file exists.
//   - (false, ErrBundleNotFound) if the path is absent.
//   - (false, wrapped err) for path-validation or unexpected I/O errors.
func Exists(ctx context.Context, path string) (bool, error) {
	st := getDefaultStorage()
	_, err := st.Stat(ctx, path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, ErrBundleNotFound
	}
	return false, fmt.Errorf("backup: stat bundle: %w", err)
}

// cleanPath canonicalises an operator-supplied path and rejects the
// obvious red flags: empty string, embedded NUL. Returns the cleaned
// path on success. Every LocalStorageOps method funnels through this
// so the underlying os.* call only ever sees a sanitised value, which
// is what CodeQL's go/path-injection rule expects to see on the way in.
func cleanPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafeBackupPath)
	}
	if strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("%w: NUL byte in path", ErrUnsafeBackupPath)
	}
	cleaned := filepath.Clean(p)
	// Segment-level ".." rejection. filepath.Clean alone collapses
	// "a/b/../c" to "a/c", but a leading "../" or a single ".."
	// component survives — which is what enables the path-injection
	// vector that CodeQL's go/path-injection rule flags. Splitting on
	// the OS separator and refusing any segment that is exactly ".."
	// is both stricter than Clean and the recognised sanitiser shape
	// that lets CodeQL prove the os.* sink is safe.
	for _, seg := range strings.Split(cleaned, string(filepath.Separator)) {
		if seg == ".." {
			return "", fmt.Errorf("%w: traversal segment in path %q", ErrUnsafeBackupPath, p)
		}
	}
	return cleaned, nil
}

// StorageOps abstracts the file-system operations the backup runner
// needs so a remote backend (S3, B2, GCS) can be swapped in without
// changing every call-site. The production implementation is
// LocalStorageOps, which wraps the standard os.* primitives.
//
// Every I/O method takes a context.Context so a remote backend can
// honor cancellation and deadlines — a long S3 download must not
// block a cancelled restore. LocalStorageOps ignores the context
// because os.* does not accept one; the signature stays uniform so
// swapping in a ctx-aware backend requires zero call-site churn.
//
// Home is the single exception: it reads an env var / getpwuid lookup
// — no I/O, no network, nothing to cancel.
type StorageOps interface {
	// Home returns the calling user's home directory.
	Home() (string, error)

	// MkdirAll ensures the given directory tree exists with perm on any
	// newly-created components.
	MkdirAll(ctx context.Context, path string, perm os.FileMode) error

	// ReadDir returns the directory entries at path. Callers sort if
	// they need a stable order.
	ReadDir(ctx context.Context, path string) ([]os.DirEntry, error)

	// Open opens path for reading.
	Open(ctx context.Context, path string) (io.ReadCloser, error)

	// Create opens path for writing with O_CREATE|O_WRONLY|O_TRUNC and
	// the given permission bits. Any existing file is truncated.
	Create(ctx context.Context, path string, perm os.FileMode) (io.WriteCloser, error)

	// CreateTemp creates a new temporary file in dir (or the OS default
	// when dir == "") matching pattern; returns a handle that is both
	// readable and writable plus the path it was created at.
	CreateTemp(ctx context.Context, dir, pattern string) (TempFile, error)

	// MkdirTemp creates a new temporary directory in dir (or the OS
	// default when dir == "") matching pattern and returns its path.
	MkdirTemp(ctx context.Context, dir, pattern string) (string, error)

	// Remove deletes a single file.
	Remove(ctx context.Context, path string) error

	// RemoveAll removes path and any children.
	RemoveAll(ctx context.Context, path string) error

	// Rename renames old to new. The runner's atomic .partial → final
	// dance relies on this being atomic on the same filesystem, which
	// os.Rename provides on Linux/macOS.
	Rename(ctx context.Context, oldPath, newPath string) error

	// Stat returns os.FileInfo for path.
	Stat(ctx context.Context, path string) (os.FileInfo, error)
}

// TempFile is the handle type returned by CreateTemp. It is both
// readable and writable, and exposes Name so the caller can reopen,
// rename, or delete the backing file later. *os.File satisfies this.
type TempFile interface {
	io.ReadWriteCloser
	Name() string
}

// LocalStorageOps is the production StorageOps backed by the host
// filesystem through os.*. It holds no state and is safe to share.
type LocalStorageOps struct{}

// Home implements StorageOps.
func (LocalStorageOps) Home() (string, error) {
	d, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("backup storage: resolve home: %w", err)
	}
	return d, nil
}

// MkdirAll implements StorageOps. Context is accepted for interface
// parity; os.MkdirAll does not honour cancellation. Errors get wrapped
// with the operation + path so a downstream log surfaces both —
// preserves errors.Is via %w.
func (LocalStorageOps) MkdirAll(_ context.Context, path string, perm os.FileMode) error {
	clean, err := cleanPath(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(clean, perm); err != nil {
		return fmt.Errorf("backup storage: mkdirall %q: %w", clean, err)
	}
	return nil
}

// ReadDir implements StorageOps.
func (LocalStorageOps) ReadDir(_ context.Context, path string) ([]os.DirEntry, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(clean)
	if err != nil {
		return nil, fmt.Errorf("backup storage: readdir %q: %w", clean, err)
	}
	return entries, nil
}

// Open implements StorageOps.
func (LocalStorageOps) Open(_ context.Context, path string) (io.ReadCloser, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, fmt.Errorf("backup storage: open %q: %w", clean, err)
	}
	return f, nil
}

// Create implements StorageOps.
func (LocalStorageOps) Create(_ context.Context, path string, perm os.FileMode) (io.WriteCloser, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return nil, fmt.Errorf("backup storage: create %q: %w", clean, err)
	}
	return f, nil
}

// CreateTemp implements StorageOps. An empty dir is forwarded as-is so
// callers retain the standard "use OS default" affordance; otherwise we
// canonicalise.
func (LocalStorageOps) CreateTemp(_ context.Context, dir, pattern string) (TempFile, error) {
	cleanDir := dir
	if dir != "" {
		var err error
		cleanDir, err = cleanPath(dir)
		if err != nil {
			return nil, err
		}
	}
	f, err := os.CreateTemp(cleanDir, pattern)
	if err != nil {
		return nil, fmt.Errorf("backup storage: createtemp %q/%q: %w", cleanDir, pattern, err)
	}
	return f, nil
}

// MkdirTemp implements StorageOps.
func (LocalStorageOps) MkdirTemp(_ context.Context, dir, pattern string) (string, error) {
	cleanDir := dir
	if dir != "" {
		var err error
		cleanDir, err = cleanPath(dir)
		if err != nil {
			return "", err
		}
	}
	d, err := os.MkdirTemp(cleanDir, pattern)
	if err != nil {
		return "", fmt.Errorf("backup storage: mkdirtemp %q/%q: %w", cleanDir, pattern, err)
	}
	return d, nil
}

// Remove implements StorageOps.
func (LocalStorageOps) Remove(_ context.Context, path string) error {
	clean, err := cleanPath(path)
	if err != nil {
		return err
	}
	if err := os.Remove(clean); err != nil {
		return fmt.Errorf("backup storage: remove %q: %w", clean, err)
	}
	return nil
}

// RemoveAll implements StorageOps.
func (LocalStorageOps) RemoveAll(_ context.Context, path string) error {
	clean, err := cleanPath(path)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(clean); err != nil {
		return fmt.Errorf("backup storage: removeall %q: %w", clean, err)
	}
	return nil
}

// Rename implements StorageOps.
func (LocalStorageOps) Rename(_ context.Context, oldPath, newPath string) error {
	cleanOld, err := cleanPath(oldPath)
	if err != nil {
		return err
	}
	cleanNew, err := cleanPath(newPath)
	if err != nil {
		return err
	}
	if err := os.Rename(cleanOld, cleanNew); err != nil {
		return fmt.Errorf("backup storage: rename %q→%q: %w", cleanOld, cleanNew, err)
	}
	return nil
}

// Stat implements StorageOps.
func (LocalStorageOps) Stat(_ context.Context, path string) (os.FileInfo, error) {
	clean, err := cleanPath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(clean)
	if err != nil {
		return nil, fmt.Errorf("backup storage: stat %q: %w", clean, err)
	}
	return info, nil
}

// defaultStorage is used by helpers that do not accept options
// (Inspect, Verify, ListBackups, Delete, cleanupStalePartials, the
// ExtractedPayload lifecycle). Tests wishing to intercept may swap it
// via SetDefaultStorage. Access is guarded by defaultStorageMu so a
// future concurrent caller does not race with an in-flight test swap.
var (
	defaultStorage   StorageOps = LocalStorageOps{}
	defaultStorageMu sync.RWMutex
)

// SetDefaultStorage swaps the package-level default for tests or for
// processes that run against a remote backend exclusively. The
// returned function restores the previous default so callers can use a
// single `defer` for teardown.
func SetDefaultStorage(s StorageOps) (restore func()) {
	defaultStorageMu.Lock()
	prev := defaultStorage
	if s == nil {
		s = LocalStorageOps{}
	}
	defaultStorage = s
	defaultStorageMu.Unlock()
	return func() {
		defaultStorageMu.Lock()
		defaultStorage = prev
		defaultStorageMu.Unlock()
	}
}

// getDefaultStorage reads the package default under the RWMutex.
func getDefaultStorage() StorageOps {
	defaultStorageMu.RLock()
	defer defaultStorageMu.RUnlock()
	return defaultStorage
}

// resolveStorage returns the caller's override if non-nil, falling
// back to the package default. Keeps call-site boilerplate to a single
// line at function entry.
func resolveStorage(s StorageOps) StorageOps {
	if s == nil {
		return getDefaultStorage()
	}
	return s
}
