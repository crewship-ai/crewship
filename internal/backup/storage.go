package backup

import (
	"context"
	"io"
	"os"
	"sync"
)

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
func (LocalStorageOps) Home() (string, error) { return os.UserHomeDir() }

// MkdirAll implements StorageOps. Context is accepted for interface
// parity; os.MkdirAll does not honour cancellation.
func (LocalStorageOps) MkdirAll(_ context.Context, path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// ReadDir implements StorageOps.
func (LocalStorageOps) ReadDir(_ context.Context, path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

// Open implements StorageOps.
func (LocalStorageOps) Open(_ context.Context, path string) (io.ReadCloser, error) {
	return os.Open(path)
}

// Create implements StorageOps.
func (LocalStorageOps) Create(_ context.Context, path string, perm os.FileMode) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
}

// CreateTemp implements StorageOps.
func (LocalStorageOps) CreateTemp(_ context.Context, dir, pattern string) (TempFile, error) {
	return os.CreateTemp(dir, pattern)
}

// MkdirTemp implements StorageOps.
func (LocalStorageOps) MkdirTemp(_ context.Context, dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}

// Remove implements StorageOps.
func (LocalStorageOps) Remove(_ context.Context, path string) error { return os.Remove(path) }

// RemoveAll implements StorageOps.
func (LocalStorageOps) RemoveAll(_ context.Context, path string) error { return os.RemoveAll(path) }

// Rename implements StorageOps.
func (LocalStorageOps) Rename(_ context.Context, oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

// Stat implements StorageOps.
func (LocalStorageOps) Stat(_ context.Context, path string) (os.FileInfo, error) {
	return os.Stat(path)
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
