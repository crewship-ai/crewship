package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// keyringFileName is the leaf of the on-disk keyring file, stored
// under ~/.crewship/. The contents are a JSON map of workspace_id →
// AES-256-GCM ciphertext (the `v1:` prefix scheme already used by the
// credstore), so a host without the ENCRYPTION_KEY cannot read stored
// passphrases even if the file itself is world-readable.
const keyringFileName = "backup-keyring.enc"

// ErrKeyringEntryNotFound is returned by GetPassphrase when no entry
// exists for the given workspace.
var ErrKeyringEntryNotFound = errors.New("backup keyring: entry not found")

// Keyring is a file-backed passphrase cache scoped to a host's
// ~/.crewship/ directory. Intentionally tiny: one level of indirection
// per workspace, AES-256-GCM at rest via internal/encryption, and a
// per-process mutex so writes within the same process serialise.
//
// The mutex does NOT cover multiple concurrent CLI invocations — two
// `crewship backup create --use-keyring` processes racing on the
// same file will last-write-wins and may clobber an entry. In
// practice the CLI is interactive, so a real race is unlikely; a
// future OS-level file lock (flock) can tighten this without
// changing the API.
//
// We do not adopt 99designs/keyring because it pulls cgo (macOS
// Security framework, libsecret) on all platforms, which conflicts
// with the single-static-binary goal the project relies on elsewhere
// (modernc.org/sqlite, ENCRYPTION_KEY env-sourced master). A future
// PR can swap this implementation for a platform-native backend via
// a build tag without touching the public API.
type Keyring struct {
	path    string
	storage StorageOps
	mu      sync.Mutex
}

// DefaultKeyring returns a Keyring rooted at ~/.crewship/
// backup-keyring.enc. The keyring ALWAYS uses LocalStorageOps,
// independent of the package-level defaultStorage: `defaultStorage`
// is the bundle backend (potentially swappable to S3/GCS in a future
// release), and sending passphrase ciphertext to a remote bundle
// store would violate the "keyring lives on the operator's host"
// contract the CLI relies on.
func DefaultKeyring(ctx context.Context) (*Keyring, error) {
	st := LocalStorageOps{}
	home, err := st.Home()
	if err != nil {
		return nil, fmt.Errorf("backup keyring: resolve home: %w", err)
	}
	dir := filepath.Join(home, ".crewship")
	if err := st.MkdirAll(ctx, dir, 0o700); err != nil {
		return nil, fmt.Errorf("backup keyring: create dir: %w", err)
	}
	return &Keyring{
		path:    filepath.Join(dir, keyringFileName),
		storage: st,
	}, nil
}

// StorePassphrase writes (or overwrites) the passphrase for
// workspaceID, re-encrypting and re-writing the whole file under the
// process lock. The file mode is forced to 0o600 on every write so a
// mistaken `umask` cannot widen permissions after the initial create.
func (k *Keyring) StorePassphrase(ctx context.Context, workspaceID, passphrase string) error {
	if workspaceID == "" {
		return fmt.Errorf("backup keyring: workspaceID required")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	entries, err := k.loadLocked(ctx)
	if err != nil {
		return err
	}
	enc, err := encryption.Encrypt(passphrase)
	if err != nil {
		return fmt.Errorf("backup keyring: encrypt: %w", err)
	}
	entries[workspaceID] = enc
	return k.saveLocked(ctx, entries)
}

// GetPassphrase returns the decrypted passphrase or
// ErrKeyringEntryNotFound.
func (k *Keyring) GetPassphrase(ctx context.Context, workspaceID string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	entries, err := k.loadLocked(ctx)
	if err != nil {
		return "", err
	}
	ct, ok := entries[workspaceID]
	if !ok {
		return "", ErrKeyringEntryNotFound
	}
	pt, err := encryption.Decrypt(ct)
	if err != nil {
		return "", fmt.Errorf("backup keyring: decrypt: %w", err)
	}
	return pt, nil
}

// Forget removes a workspace's passphrase. Non-existence is not an
// error — the caller usually does not care whether a delete was a
// no-op.
func (k *Keyring) Forget(ctx context.Context, workspaceID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	entries, err := k.loadLocked(ctx)
	if err != nil {
		return err
	}
	delete(entries, workspaceID)
	return k.saveLocked(ctx, entries)
}

// loadLocked reads the file. An absent file returns an empty map so
// the first StorePassphrase on a fresh install succeeds. Must be
// called with k.mu held.
func (k *Keyring) loadLocked(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	r, err := k.storage.Open(ctx, k.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("backup keyring: open: %w", err)
	}
	defer func() { _ = r.Close() }()
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("backup keyring: read: %w", err)
	}
	if len(body) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("backup keyring: parse: %w", err)
	}
	return out, nil
}

// saveLocked writes the map atomically via .partial + rename so a
// crash between allocations does not corrupt the file.
func (k *Keyring) saveLocked(ctx context.Context, entries map[string]string) error {
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("backup keyring: marshal: %w", err)
	}
	partial := k.path + ".partial"
	w, err := k.storage.Create(ctx, partial, 0o600)
	if err != nil {
		return fmt.Errorf("backup keyring: create partial: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		_ = w.Close()
		_ = k.storage.Remove(ctx, partial)
		return fmt.Errorf("backup keyring: write: %w", err)
	}
	if err := w.Close(); err != nil {
		_ = k.storage.Remove(ctx, partial)
		return fmt.Errorf("backup keyring: close: %w", err)
	}
	if err := k.storage.Rename(ctx, partial, k.path); err != nil {
		_ = k.storage.Remove(ctx, partial)
		return fmt.Errorf("backup keyring: rename: %w", err)
	}
	return nil
}
