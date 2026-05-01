package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

// TestKeyring_ConcurrentInProcess writes a distinct passphrase from each
// of N goroutines using a shared Keyring instance, then verifies every
// entry round-trips. Without the in-process mutex the load-modify-save
// sequence interleaves and earlier writes vanish; with it, all N
// passphrases are recoverable.
func TestKeyring_ConcurrentInProcess(t *testing.T) {
	withTestEncryptionKey(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	k, err := DefaultKeyring(context.Background())
	if err != nil {
		t.Fatalf("DefaultKeyring: %v", err)
	}

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			ws := fmt.Sprintf("ws-%03d", i)
			if err := k.StorePassphrase(context.Background(), ws, fmt.Sprintf("pass-%d", i)); err != nil {
				errs <- fmt.Errorf("store %s: %w", ws, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent store: %v", err)
	}

	for i := 0; i < N; i++ {
		ws := fmt.Sprintf("ws-%03d", i)
		got, err := k.GetPassphrase(context.Background(), ws)
		if err != nil {
			t.Errorf("Get %s: %v", ws, err)
			continue
		}
		want := fmt.Sprintf("pass-%d", i)
		if got != want {
			t.Errorf("Get %s: got %q want %q", ws, got, want)
		}
	}
}

// TestKeyring_ConcurrentCrossInstance simulates two CLI processes
// hitting the same keyring file by constructing two independent
// Keyring instances on the same path. Each instance has its own
// fileLock fd, so this exercises the cross-process flock path that
// the in-process mutex alone could not cover. With CRE-130 fixed,
// every passphrase from every instance round-trips.
func TestKeyring_ConcurrentCrossInstance(t *testing.T) {
	withTestEncryptionKey(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	const Instances = 4
	const PerInstance = 25

	rings := make([]*Keyring, Instances)
	for i := range rings {
		k, err := DefaultKeyring(context.Background())
		if err != nil {
			t.Fatalf("DefaultKeyring %d: %v", i, err)
		}
		rings[i] = k
	}

	var wg sync.WaitGroup
	wg.Add(Instances * PerInstance)
	errs := make(chan error, Instances*PerInstance)
	for i, k := range rings {
		for j := 0; j < PerInstance; j++ {
			go func(k *Keyring, i, j int) {
				defer wg.Done()
				ws := fmt.Sprintf("inst-%d-ws-%02d", i, j)
				if err := k.StorePassphrase(context.Background(), ws, fmt.Sprintf("p-%d-%d", i, j)); err != nil {
					errs <- fmt.Errorf("store %s: %w", ws, err)
				}
			}(k, i, j)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("cross-instance store: %v", err)
	}

	verifier := rings[0]
	for i := 0; i < Instances; i++ {
		for j := 0; j < PerInstance; j++ {
			ws := fmt.Sprintf("inst-%d-ws-%02d", i, j)
			got, err := verifier.GetPassphrase(context.Background(), ws)
			if err != nil {
				t.Errorf("Get %s: %v", ws, err)
				continue
			}
			want := fmt.Sprintf("p-%d-%d", i, j)
			if got != want {
				t.Errorf("Get %s: got %q want %q (lost write across instances)", ws, got, want)
			}
		}
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
