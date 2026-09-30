package resourcelifecycle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const InstanceLabel = "crewship.instance-id"

// ErrIdentityInUse means another live process already holds this database's
// installation identity in this data directory: a copied database, or a second
// server pointed at the same one. Automatic cleanup must stay disabled.
var ErrIdentityInUse = errors.New("installation identity is held by another running process")

// Identity is the installation label value plus the lock that proves this
// process is its only holder. Close releases the lock at shutdown.
type Identity struct {
	ID   string
	lock *os.File
}

func (i *Identity) Close() error {
	if i == nil || i.lock == nil {
		return nil
	}
	return i.lock.Close()
}

// LoadIdentity binds the installation identity to both this database and this
// data directory. Neither alone is enough: several dev servers share
// ~/.crewship, and a database copied elsewhere must not inherit authority over
// the original's containers. The database keeps only a random nonce; the
// identity itself lives in the data directory under that nonce, outside SQLite
// and workspace backups. Never copy the installations directory to create a
// second installation sharing a daemon.
func LoadIdentity(ctx context.Context, root string, db *sql.DB) (*Identity, error) {
	nonce, err := databaseNonce(ctx, db)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "installations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, nonce+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	held, err := tryExclusiveLock(lock)
	if err != nil || !held {
		lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrIdentityInUse
	}
	id, err := readOrCreateID(filepath.Join(dir, nonce))
	if err != nil {
		lock.Close()
		return nil, err
	}
	return &Identity{ID: id, lock: lock}, nil
}

func databaseNonce(ctx context.Context, db *sql.DB) (string, error) {
	fresh, err := randomHex()
	if err != nil {
		return "", err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO resource_cleanup_installation(id,db_nonce) VALUES(1,?) ON CONFLICT(id) DO NOTHING`, fresh); err != nil {
		return "", fmt.Errorf("record database nonce: %w", err)
	}
	var nonce string
	if err := db.QueryRowContext(ctx, `SELECT db_nonce FROM resource_cleanup_installation WHERE id=1`).Scan(&nonce); err != nil {
		return "", fmt.Errorf("read database nonce: %w", err)
	}
	if !validHex(nonce) {
		return "", fmt.Errorf("invalid database nonce")
	}
	return nonce, nil
}

func randomHex() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func validHex(s string) bool {
	decoded, err := hex.DecodeString(s)
	return err == nil && len(decoded) == 32
}

func readOrCreateID(path string) (string, error) {
	read := func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		id := strings.TrimSpace(string(b))
		if !validHex(id) {
			return "", fmt.Errorf("invalid installation identity")
		}
		return id, nil
	}
	if id, err := read(); err == nil {
		return id, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	id, err := randomHex()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".instance-id-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(id + "\n"); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	// Link publishes a complete file without overwriting a competing startup.
	if err = os.Link(f.Name(), path); err != nil && !os.IsExist(err) {
		return "", err
	}
	return read()
}

func WithInstanceLabel(labels map[string]string, instance string) map[string]string {
	// Explicit presence masks any label inherited from a cache/custom image,
	// even when identity is unavailable and automatic cleanup must be disabled.
	labels[InstanceLabel] = instance
	return labels
}
