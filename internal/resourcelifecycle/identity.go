package resourcelifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
// installation identity: a second server pointed at the same database file.
// Automatic cleanup must stay disabled.
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

// LoadIdentity binds the installation identity to this database, its location
// and this data directory. None alone is enough: several dev servers share
// ~/.crewship, and a copied database carries the original's nonce. The
// database keeps only a random nonce; the data directory keeps the identity
// under that nonce together with the database location that claimed it. A
// database found at another location (a copy, or a moved file) is re-keyed
// with a fresh nonce and gets a new identity, so it never inherits authority
// over the original's containers, even after the original stops. location is
// the canonical database location from DatabaseLocation. Never copy the
// installations directory to create a second installation sharing a daemon.
func LoadIdentity(ctx context.Context, root string, db *sql.DB, location string) (*Identity, error) {
	if location == "" {
		return nil, fmt.Errorf("database location is required")
	}
	sum := sha256.Sum256([]byte(location))
	dir := filepath.Join(root, "installations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return loadIdentity(ctx, dir, db, hex.EncodeToString(sum[:]), false)
}

var errLocationClaimed = errors.New("installation identity belongs to another database location")

func loadIdentity(ctx context.Context, dir string, db *sql.DB, locator string, rekeyed bool) (*Identity, error) {
	nonce, err := databaseNonce(ctx, db)
	if err != nil {
		return nil, err
	}
	owner, err := os.ReadFile(filepath.Join(dir, nonce+".db"))
	switch {
	case err == nil && strings.TrimSpace(string(owner)) != locator:
		// Same nonce, different database location: take a new nonce rather
		// than the original's identity.
		if nonce, err = rekeyDatabase(ctx, db); err != nil {
			return nil, err
		}
	case err != nil && !os.IsNotExist(err):
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
	// Re-check under the lock: a copy starting at the same moment may have
	// claimed this nonce first. Re-key once and take a fresh identity.
	if err := claimLocation(filepath.Join(dir, nonce+".db"), locator); err != nil {
		lock.Close()
		if !errors.Is(err, errLocationClaimed) || rekeyed {
			return nil, err
		}
		if _, err := rekeyDatabase(ctx, db); err != nil {
			return nil, err
		}
		return loadIdentity(ctx, dir, db, locator, true)
	}
	id, err := readOrCreateID(filepath.Join(dir, nonce))
	if err != nil {
		lock.Close()
		return nil, err
	}
	return &Identity{ID: id, lock: lock}, nil
}

// DatabaseLocation canonicalizes a database URL so that the same database
// always yields the same location. SQLite file URLs resolve to an absolute,
// symlink-free path without query options; other URLs keep scheme, host and
// path but never credentials.
func DatabaseLocation(databaseURL string) (string, error) {
	raw := strings.TrimSpace(databaseURL)
	if raw == "" {
		return "", fmt.Errorf("empty database URL")
	}
	if strings.Contains(raw, "://") && !strings.HasPrefix(raw, "file://") {
		scheme, rest, _ := strings.Cut(raw, "://")
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		rest, _, _ = strings.Cut(rest, "?")
		return scheme + "://" + rest, nil
	}
	path := strings.TrimPrefix(strings.TrimPrefix(raw, "file://"), "file:")
	path, _, _ = strings.Cut(path, "?")
	if path == "" || path == ":memory:" {
		return "", fmt.Errorf("database has no stable location")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve the directory, not the file: the database file may not exist
	// yet on a first start, but its directory does.
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return "file:" + filepath.Join(dir, filepath.Base(abs)), nil
}

// claimLocation records which database location owns a nonce, or confirms it.
// Called under the nonce lock, so the check-then-write cannot race a peer.
func claimLocation(path, locator string) error {
	owner, err := os.ReadFile(path)
	if err == nil {
		if strings.TrimSpace(string(owner)) != locator {
			return errLocationClaimed
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(path, []byte(locator+"\n"), 0o600)
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

func rekeyDatabase(ctx context.Context, db *sql.DB) (string, error) {
	fresh, err := randomHex()
	if err != nil {
		return "", err
	}
	if _, err := db.ExecContext(ctx, `UPDATE resource_cleanup_installation SET db_nonce=? WHERE id=1`, fresh); err != nil {
		return "", fmt.Errorf("re-key copied database: %w", err)
	}
	return fresh, nil
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
