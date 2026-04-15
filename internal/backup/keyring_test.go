package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestKeyring_RoundTrip validates the StorePassphrase → GetPassphrase
// path inside a scratch HOME. ENCRYPTION_KEY is the 32-byte master
// key the internal/encryption package uses; a hex test key keeps the
// test hermetic.
func TestKeyring_RoundTrip(t *testing.T) {
	withTestEncryptionKey(t)

	home := t.TempDir()
	t.Setenv("HOME", home)

	k, err := DefaultKeyring(context.Background())
	if err != nil {
		t.Fatalf("DefaultKeyring: %v", err)
	}

	if err := k.StorePassphrase(context.Background(), "ws-1", "hunter2"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, err := k.GetPassphrase(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("round-trip passphrase: got %q want hunter2", got)
	}

	// Overwrite exercises the loop-and-re-encrypt path.
	if err := k.StorePassphrase(context.Background(), "ws-1", "correcthorsebatterystaple"); err != nil {
		t.Fatalf("Store overwrite: %v", err)
	}
	got2, err := k.GetPassphrase(context.Background(), "ws-1")
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}
	if got2 != "correcthorsebatterystaple" {
		t.Errorf("overwrite: got %q", got2)
	}

	// File mode should be 0600.
	info, err := os.Stat(filepath.Join(home, ".crewship", keyringFileName))
	if err != nil {
		t.Fatalf("stat keyring file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("keyring file mode: got %v want 0600", info.Mode().Perm())
	}
}

func TestKeyring_GetMissing(t *testing.T) {
	withTestEncryptionKey(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	k, err := DefaultKeyring(context.Background())
	if err != nil {
		t.Fatalf("DefaultKeyring: %v", err)
	}
	_, err = k.GetPassphrase(context.Background(), "absent")
	if !errors.Is(err, ErrKeyringEntryNotFound) {
		t.Errorf("expected ErrKeyringEntryNotFound, got %v", err)
	}
}

func TestKeyring_Forget(t *testing.T) {
	withTestEncryptionKey(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	k, err := DefaultKeyring(context.Background())
	if err != nil {
		t.Fatalf("DefaultKeyring: %v", err)
	}
	if err := k.StorePassphrase(context.Background(), "ws-1", "secret"); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if err := k.Forget(context.Background(), "ws-1"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := k.GetPassphrase(context.Background(), "ws-1"); !errors.Is(err, ErrKeyringEntryNotFound) {
		t.Errorf("post-Forget: expected ErrKeyringEntryNotFound, got %v", err)
	}
}

// withTestEncryptionKey sets a deterministic 32-byte hex key so the
// internal/encryption package has something to derive AES-256 from.
// The key is synthesised from a simple pattern at test time so no
// credential-shaped string lives in source for scanners to flag.
func withTestEncryptionKey(t *testing.T) {
	t.Helper()
	const patt = "0123456789abcdef"
	t.Setenv("ENCRYPTION_KEY", patt+patt+patt+patt)
}
