package backup

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"testing"
)

// TestLocalStorageOps_RoundTrip walks LocalStorageOps through the
// operations the backup runner actually uses (MkdirAll → CreateTemp →
// write → Open → read → Rename → Stat → ReadDir → Remove) on a real
// temp directory. Nothing exotic — the point is catching a typo in the
// interface that slips past compilation.
func TestLocalStorageOps_RoundTrip(t *testing.T) {
	ops := LocalStorageOps{}
	root := t.TempDir()

	sub := filepath.Join(root, "nested", "dir")
	if err := ops.MkdirAll(t.Context(), sub, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// CreateTemp inside sub, write a blob, close.
	tf, err := ops.CreateTemp(t.Context(), sub, "blob-*.bin")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	payload := []byte("hello storage")
	if _, err := tf.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tf.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tempPath := tf.Name()

	// Open for read, check content.
	r, err := ops.Open(t.Context(), tempPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("round-trip content mismatch: got %q want %q", got, payload)
	}

	// Stat → size matches payload.
	info, err := ops.Stat(t.Context(), tempPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() != int64(len(payload)) {
		t.Fatalf("Stat size: got %d want %d", info.Size(), len(payload))
	}

	// Rename to deterministic path, then ReadDir surfaces it.
	final := filepath.Join(sub, "final.bin")
	if err := ops.Rename(t.Context(), tempPath, final); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	entries, err := ops.ReadDir(t.Context(), sub)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Name() == "final.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ReadDir missing renamed file")
	}

	// Create (truncating) replaces content.
	w, err := ops.Create(t.Context(), final, 0o600)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("overwritten")); err != nil {
		t.Fatalf("Write on Create: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close on Create: %v", err)
	}
	r2, err := ops.Open(t.Context(), final)
	if err != nil {
		t.Fatalf("Open final: %v", err)
	}
	got2, err := io.ReadAll(r2)
	if err != nil {
		t.Fatalf("ReadAll final: %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("Close final: %v", err)
	}
	if string(got2) != "overwritten" {
		t.Fatalf("Create truncate failed: got %q", got2)
	}

	// Remove + RemoveAll.
	if err := ops.Remove(t.Context(), final); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := ops.RemoveAll(t.Context(), filepath.Join(root, "nested")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	// errors.Is instead of os.IsNotExist because LocalStorageOps now
	// wraps os errors with fmt.Errorf %w — os.IsNotExist only unwraps
	// *fs.PathError / *os.LinkError, so the chain is invisible to it.
	if _, err := ops.Stat(t.Context(), filepath.Join(root, "nested")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist after RemoveAll, got %v", err)
	}

	// MkdirTemp + RemoveAll cleanup.
	td, err := ops.MkdirTemp(t.Context(), root, "tmp-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if _, err := ops.Stat(t.Context(), td); err != nil {
		t.Fatalf("Stat MkdirTemp: %v", err)
	}
	if err := ops.RemoveAll(t.Context(), td); err != nil {
		t.Fatalf("RemoveAll tempdir: %v", err)
	}

	// Home does not error on a normal dev host.
	if _, err := ops.Home(); err != nil {
		t.Fatalf("Home: %v", err)
	}
}

// TestSetDefaultStorage_RestoresPrevious verifies the swap/restore
// helper is symmetric — important so test fixtures can use a single
// `defer` without corrupting the package-level default for later tests.
func TestSetDefaultStorage_RestoresPrevious(t *testing.T) {
	orig := getDefaultStorage()
	sentinel := recordingStorage{LocalStorageOps{}}

	restore := SetDefaultStorage(sentinel)
	if _, ok := getDefaultStorage().(recordingStorage); !ok {
		t.Fatalf("SetDefaultStorage did not install override: %T", getDefaultStorage())
	}
	restore()
	if getDefaultStorage() != orig {
		t.Fatalf("restore() did not revert defaultStorage")
	}

	// Nil input must fall back to LocalStorageOps rather than panic
	// later when a helper dereferences a nil interface.
	restore = SetDefaultStorage(nil)
	if _, ok := getDefaultStorage().(LocalStorageOps); !ok {
		t.Fatalf("nil input did not resolve to LocalStorageOps: %T", getDefaultStorage())
	}
	restore()
	if getDefaultStorage() != orig {
		t.Fatalf("restore() after nil input did not revert defaultStorage")
	}
}

// recordingStorage is a trivial decorator used by the test above — the
// concrete type is what we assert against after SetDefaultStorage.
type recordingStorage struct{ LocalStorageOps }
