package bbolt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
)

var _ provider.StateProvider = (*Provider)(nil)

// ErrLocked reports that the state file's advisory lock is held by another
// process and did not become free within the wait window. Callers get it
// wrapped in an error that names the file; it exists as a sentinel so startup
// code can tell "another instance owns this state" apart from a genuine I/O
// failure without matching on error text.
var ErrLocked = errors.New("state database is locked by another process")

// lockProbe is the first, deliberately tiny bolt.Open timeout. Its only job is
// to find out whether the flock is contended without paying the full wait: on
// an uncontended start (every normal boot) it costs nothing, and on a
// contended one it lets us log WHICH file we are about to block on before we
// block. bbolt has no "is it locked" API, so a short open is the probe.
const lockProbe = 150 * time.Millisecond

// lockWait bounds the total time New spends waiting for the flock, probe
// included. Ten seconds covers the case this timeout is actually for — a
// `systemctl restart` where the outgoing process is still flushing its last
// transaction — while keeping a genuine second-instance collision to a bounded
// failure instead of a permanent hang.
//
// A var, not a const, purely so the tests can shrink it; nothing in production
// assigns to it.
var lockWait = 10 * time.Second

// Provider implements StateProvider using an embedded bbolt key-value database.
type Provider struct {
	db *bolt.DB
}

// New opens or creates a bbolt database at the given file path.
//
// B-04: this used to call bolt.Open with Timeout: 0, which means "wait for the
// flock forever". Starting a second crewshipd on a host that already had one
// running therefore produced the worst available outcome — the process stayed
// alive, never bound its HTTP port, and logged NOTHING after "docker provider
// disabled". An operator watching it for 25 s could not tell a wedged start
// from a slow one; only a signal ended it.
//
// So the open is now probe → log → bounded wait → named error. Even once the
// paths are derived per data dir (see config.defaultPathsFor) contention
// remains reachable — two starts of the SAME instance, a stale holder that
// outlived its unit, a plain operator mistake — and for all of those a loud,
// bounded failure beats silence.
func New(path string) (*Provider, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}

	wait := lockWait
	probe := lockProbe
	if probe > wait {
		probe = wait
	}

	db, err := openLocked(path, probe)
	if err == nil {
		return &Provider{db: db}, nil
	}
	if !errors.Is(err, bolterrors.ErrTimeout) {
		return nil, fmt.Errorf("open bbolt %s: %w", path, err)
	}

	// The lock is held. Say so BEFORE blocking — this log line is the half of
	// the fix that matters, because it turns the 25 s of silence into a
	// sentence naming the file.
	//
	// slog.Default() rather than an injected logger: cmd_start calls
	// slog.SetDefault with the configured handler well before it builds the
	// providers, and threading a logger through provider.New would change a
	// signature shared with the other state providers for one message.
	slog.Default().Warn("waiting for state database lock — another crewship instance appears to be running",
		"path", path, "timeout", wait)

	if remaining := wait - probe; remaining > 0 {
		db, err = openLocked(path, remaining)
		if err == nil {
			slog.Default().Info("state database lock acquired", "path", path)
			return &Provider{db: db}, nil
		}
		if !errors.Is(err, bolterrors.ErrTimeout) {
			return nil, fmt.Errorf("open bbolt %s: %w", path, err)
		}
	}

	// No PID here on purpose: bbolt takes a POSIX advisory lock, and mapping
	// that back to a holder means parsing /proc/locks against the file's
	// device+inode (Linux only, needs syscall.Stat_t) or shelling out. Neither
	// is cheap or portable enough to belong on the startup path, and the
	// operator only needs one command to close the gap — so we name the file,
	// name the likely cause, and hand them that command.
	return nil, fmt.Errorf("%w: %s (waited %s; find the holder with: lsof %s)",
		ErrLocked, path, wait, path)
}

// openLocked opens the bbolt file with a bounded flock timeout. A zero or
// negative timeout would restore the block-forever behaviour of B-04, so it is
// clamped to the probe duration rather than passed through.
func openLocked(path string, timeout time.Duration) (*bolt.DB, error) {
	if timeout <= 0 {
		timeout = lockProbe
	}
	return bolt.Open(path, 0600, &bolt.Options{
		NoSync:  false,
		Timeout: timeout,
	})
}

// Get retrieves the value for key in the named bucket, or nil if not found.
func (p *Provider) Get(_ context.Context, bucket, key string) ([]byte, error) {
	var val []byte
	err := p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		v := b.Get([]byte(key))
		if v != nil {
			val = make([]byte, len(v))
			copy(val, v)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt get %s/%s: %w", bucket, key, err)
	}
	return val, nil
}

// Set stores value under key in the named bucket, creating the bucket if needed.
func (p *Provider) Set(_ context.Context, bucket, key string, value []byte) error {
	return p.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucket))
		if err != nil {
			return fmt.Errorf("create bucket %s: %w", bucket, err)
		}
		return b.Put([]byte(key), value)
	})
}

// Delete removes the key from the named bucket.
func (p *Provider) Delete(_ context.Context, bucket, key string) error {
	return p.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}

// List returns all key-value pairs in the named bucket.
func (p *Provider) List(_ context.Context, bucket string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	err := p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			val := make([]byte, len(v))
			copy(val, v)
			result[string(k)] = val
			return nil
		})
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt list %s: %w", bucket, err)
	}
	return result, nil
}

// ListByPrefix returns all key-value pairs in the bucket whose keys start with prefix.
func (p *Provider) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	pfx := []byte(prefix)
	err := p.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Seek(pfx); k != nil && bytes.HasPrefix(k, pfx); k, v = c.Next() {
			val := make([]byte, len(v))
			copy(val, v)
			result[string(k)] = val
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("bbolt list prefix %s/%s: %w", bucket, prefix, err)
	}
	return result, nil
}

// Close closes the underlying bbolt database.
func (p *Provider) Close() error {
	return p.db.Close()
}
