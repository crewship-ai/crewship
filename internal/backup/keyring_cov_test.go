package backup

// Coverage tests for keyring.go — DefaultKeyring error paths, the
// encrypt/decrypt failure branches, loadLocked's malformed-file
// handling, and saveLocked's atomic-write failure ladder (via an
// injectable StorageOps).

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultKeyring_ErrorPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("home resolution failure", func(t *testing.T) {
		t.Setenv("HOME", "")
		_, err := DefaultKeyring(ctx)
		if err == nil || !strings.Contains(err.Error(), "resolve home") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("dot-crewship dir creation failure", func(t *testing.T) {
		// HOME pointing at a regular FILE makes MkdirAll fail.
		fakeHome := filepath.Join(t.TempDir(), "homefile")
		if err := os.WriteFile(fakeHome, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", fakeHome)
		_, err := DefaultKeyring(ctx)
		if err == nil || !strings.Contains(err.Error(), "create dir") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestKeyring_StoreValidationAndEncryptFailure(t *testing.T) {
	ctx := context.Background()
	withTestEncryptionKey(t)
	t.Setenv("HOME", t.TempDir())
	k, err := DefaultKeyring(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := k.StorePassphrase(ctx, "", "p"); err == nil || !strings.Contains(err.Error(), "workspaceID required") {
		t.Fatalf("err = %v", err)
	}

	// Invalid master key → encryption.Encrypt fails inside the lock.
	t.Setenv("ENCRYPTION_KEY", "way-too-short")
	err = k.StorePassphrase(ctx, "ws-1", "p")
	if err == nil || !strings.Contains(err.Error(), "encrypt") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyring_DecryptFailureOnKeyRotation(t *testing.T) {
	ctx := context.Background()
	withTestEncryptionKey(t)
	t.Setenv("HOME", t.TempDir())
	k, err := DefaultKeyring(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.StorePassphrase(ctx, "ws-rot", "secret"); err != nil {
		t.Fatal(err)
	}
	// Rotate the master key out from under the stored entry.
	const otherPatt = "fedcba9876543210"
	t.Setenv("ENCRYPTION_KEY", otherPatt+otherPatt+otherPatt+otherPatt)
	_, err = k.GetPassphrase(ctx, "ws-rot")
	if err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyring_LoadLockedMalformedFile(t *testing.T) {
	ctx := context.Background()
	withTestEncryptionKey(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	k, err := DefaultKeyring(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ringPath := filepath.Join(home, ".crewship", keyringFileName)

	t.Run("garbage JSON", func(t *testing.T) {
		if err := os.WriteFile(ringPath, []byte("{corrupted"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := k.GetPassphrase(ctx, "ws")
		if err == nil || !strings.Contains(err.Error(), "parse") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty file behaves like a fresh keyring", func(t *testing.T) {
		if err := os.WriteFile(ringPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := k.GetPassphrase(ctx, "ws")
		if !errors.Is(err, ErrKeyringEntryNotFound) {
			t.Fatalf("err = %v, want ErrKeyringEntryNotFound", err)
		}
		if err := k.StorePassphrase(ctx, "ws", "p"); err != nil {
			t.Fatalf("store on empty file: %v", err)
		}
		got, err := k.GetPassphrase(ctx, "ws")
		if err != nil || got != "p" {
			t.Fatalf("get = (%q, %v)", got, err)
		}
	})
}

// keyringStubStorage lets each StorageOps call be failed independently.
// Only the methods the keyring exercises are functional; the rest panic
// so an unexpected dependency is loud.
type keyringStubStorage struct {
	stubStorageOpsForDefaultDir // panicking defaults

	openErr   error
	openBody  io.ReadCloser
	createErr error
	writer    io.WriteCloser
	removeLog *[]string
	renameErr error
}

func (s keyringStubStorage) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	if s.openBody != nil {
		return s.openBody, nil
	}
	return nil, os.ErrNotExist
}
func (s keyringStubStorage) Create(_ context.Context, _ string, _ os.FileMode) (io.WriteCloser, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	return s.writer, nil
}
func (s keyringStubStorage) Remove(_ context.Context, p string) error {
	if s.removeLog != nil {
		*s.removeLog = append(*s.removeLog, p)
	}
	return nil
}
func (s keyringStubStorage) Rename(_ context.Context, _, _ string) error { return s.renameErr }

// failWriter fails on Write or Close depending on configuration.
type failWriter struct {
	writeErr error
	closeErr error
}

func (w *failWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return len(p), nil
}
func (w *failWriter) Close() error { return w.closeErr }

func TestKeyring_SaveLockedFailureLadder(t *testing.T) {
	ctx := context.Background()
	withTestEncryptionKey(t)

	newRing := func(st StorageOps) *Keyring {
		return &Keyring{path: "/ring/backup-keyring.enc", storage: st}
	}

	t.Run("create partial fails", func(t *testing.T) {
		k := newRing(keyringStubStorage{createErr: errors.New("disk full")})
		err := k.StorePassphrase(ctx, "ws", "p")
		if err == nil || !strings.Contains(err.Error(), "create partial") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("write fails and partial is removed", func(t *testing.T) {
		var removed []string
		k := newRing(keyringStubStorage{
			writer:    &failWriter{writeErr: errors.New("io fault")},
			removeLog: &removed,
		})
		err := k.StorePassphrase(ctx, "ws", "p")
		if err == nil || !strings.Contains(err.Error(), "keyring: write") {
			t.Fatalf("err = %v", err)
		}
		if len(removed) != 1 || !strings.HasSuffix(removed[0], ".partial") {
			t.Errorf("partial not cleaned up: %v", removed)
		}
	})
	t.Run("close fails and partial is removed", func(t *testing.T) {
		var removed []string
		k := newRing(keyringStubStorage{
			writer:    &failWriter{closeErr: errors.New("flush fault")},
			removeLog: &removed,
		})
		err := k.StorePassphrase(ctx, "ws", "p")
		if err == nil || !strings.Contains(err.Error(), "keyring: close") {
			t.Fatalf("err = %v", err)
		}
		if len(removed) != 1 {
			t.Errorf("partial not cleaned up: %v", removed)
		}
	})
	t.Run("rename fails and partial is removed", func(t *testing.T) {
		var removed []string
		k := newRing(keyringStubStorage{
			writer:    &failWriter{},
			renameErr: errors.New("cross-device"),
			removeLog: &removed,
		})
		err := k.StorePassphrase(ctx, "ws", "p")
		if err == nil || !strings.Contains(err.Error(), "keyring: rename") {
			t.Fatalf("err = %v", err)
		}
		if len(removed) != 1 {
			t.Errorf("partial not cleaned up: %v", removed)
		}
	})
	t.Run("open failure other than not-exist surfaces", func(t *testing.T) {
		k := newRing(keyringStubStorage{openErr: errors.New("permission denied")})
		err := k.StorePassphrase(ctx, "ws", "p")
		if err == nil || !strings.Contains(err.Error(), "keyring: open") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("read failure surfaces", func(t *testing.T) {
		k := newRing(keyringStubStorage{
			openBody: io.NopCloser(errReaderCov{errors.New("torn read")}),
		})
		err := k.StorePassphrase(ctx, "ws", "p")
		if err == nil || !strings.Contains(err.Error(), "keyring: read") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestKeyring_WithLocksFlockError(t *testing.T) {
	withTestEncryptionKey(t)
	// A flock whose backing path cannot be created makes Lock fail.
	k := &Keyring{
		path:    "/nonexistent-cov-dir/backup-keyring.enc",
		storage: LocalStorageOps{},
		flock:   newFileLock("/nonexistent-cov-dir/backup-keyring.enc"),
	}
	err := k.StorePassphrase(context.Background(), "ws", "p")
	if err == nil || !strings.Contains(err.Error(), "keyring: lock") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyring_ForgetLoadError(t *testing.T) {
	withTestEncryptionKey(t)
	k := &Keyring{path: "/ring/x.enc", storage: keyringStubStorage{openErr: errors.New("io down")}}
	err := k.Forget(context.Background(), "ws")
	if err == nil || !strings.Contains(err.Error(), "keyring: open") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyring_ForgetMissingEntryIsNoop(t *testing.T) {
	ctx := context.Background()
	withTestEncryptionKey(t)
	t.Setenv("HOME", t.TempDir())
	k, err := DefaultKeyring(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Forget(ctx, "never-stored"); err != nil {
		t.Fatalf("Forget on absent entry: %v", err)
	}
}
