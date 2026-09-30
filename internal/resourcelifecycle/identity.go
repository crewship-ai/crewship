package resourcelifecycle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
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
	return loadIdentity(ctx, dir, db, hex.EncodeToString(sum[:]))
}

var errLocationClaimed = errors.New("installation identity belongs to another database location")

// testHookBeforeRekey lets a test hold concurrent starts between reading the
// nonce's owner and re-keying, the window the compare-and-swap protects.
var testHookBeforeRekey func()

// loadIdentity settles on exactly one identity per database even when several
// starts of the same copy race: re-keying is a compare-and-swap on the nonce,
// so a loser adopts the winner's nonce and then meets its lock.
func loadIdentity(ctx context.Context, dir string, db *sql.DB, locator string) (*Identity, error) {
	for attempt := 0; attempt < 4; attempt++ {
		nonce, err := databaseNonce(ctx, db)
		if err != nil {
			return nil, err
		}
		owner, err := os.ReadFile(filepath.Join(dir, nonce+".db"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && strings.TrimSpace(string(owner)) != locator {
			if testHookBeforeRekey != nil {
				testHookBeforeRekey()
			}
			// Same nonce, different database location: take a new nonce
			// rather than the original's identity.
			if err := rekeyDatabase(ctx, db, nonce); err != nil {
				return nil, err
			}
			continue
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
		// Re-check under the lock: another database may have claimed this
		// nonce between the read above and the lock.
		if err := claimLocation(filepath.Join(dir, nonce+".db"), locator); err != nil {
			lock.Close()
			if !errors.Is(err, errLocationClaimed) {
				return nil, err
			}
			if err := rekeyDatabase(ctx, db, nonce); err != nil {
				return nil, err
			}
			continue
		}
		id, err := readOrCreateID(filepath.Join(dir, nonce))
		if err != nil {
			lock.Close()
			return nil, err
		}
		return &Identity{ID: id, lock: lock}, nil
	}
	return nil, fmt.Errorf("installation identity did not settle")
}

// DatabaseLocation canonicalizes a database URL so that the same database
// always yields the same location. SQLite file URLs resolve to the absolute,
// symlink-free path of the database file itself (a symlink to the file is the
// same database) without query options; in-memory databases have no stable
// location. Other URLs keep scheme, host and path but never credentials.
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
	path, query, _ := strings.Cut(path, "?")
	params, err := url.ParseQuery(query)
	if err != nil {
		return "", fmt.Errorf("parse database URL options: %w", err)
	}
	if strings.EqualFold(params.Get("mode"), "memory") || strings.EqualFold(params.Get("vfs"), "memdb") ||
		path == "" || strings.HasPrefix(path, ":memory:") {
		return "", fmt.Errorf("database has no stable location")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// The file exists by the time identity is loaded (after migrations);
	// resolving it whole makes a symlinked path the same database.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return "file:" + resolved, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
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

// rekeyDatabase replaces the nonce only if it is still the one this start
// read. A concurrent start of the same database that re-keyed first wins; the
// caller then re-reads and joins the winner's nonce instead of forking a
// second identity for one database.
func rekeyDatabase(ctx context.Context, db *sql.DB, expected string) error {
	fresh, err := randomHex()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `UPDATE resource_cleanup_installation SET db_nonce=? WHERE id=1 AND db_nonce=?`, fresh, expected); err != nil {
		return fmt.Errorf("re-key copied database: %w", err)
	}
	return nil
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
