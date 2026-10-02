// Package writerlease enforces one cooperating writing server per local database.
// The kernel owns the lease for the lifetime of a private file descriptor, so
// process exit releases it without a heartbeat or a stale-lock deletion policy.
package writerlease

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrHeld               = errors.New("writer lease: another writing process owns this database")
	ErrLost               = errors.New("writer lease: database ownership was lost")
	ErrUnsupportedStorage = errors.New("writer lease: database requires supported local storage; shared storage requires distributed fencing")
)

type Lease struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	identity os.FileInfo
}

// Acquire does not wait and never unlinks the database or a shared lock anchor.
// Locking the database itself also catches symlink and hard-link path aliases.
func Acquire(path string) (*Lease, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = supportedStorage(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open writer lease: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	identity, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !identity.Mode().IsRegular() {
		return nil, fmt.Errorf("writer lease: database is not a regular file")
	}
	if err = supportedStorage(path); err != nil {
		return nil, err
	}
	if err = lock(file); err != nil {
		return nil, err
	}
	lease := &Lease{file: file, path: path, identity: identity}
	if err = lease.Verify(); err != nil {
		return nil, err
	}
	keep = true
	return lease, nil
}

// Verify proves that the still-private, still-open locked handle and the
// configured database name refer to the original file. No lock is reacquired:
// reacquiring after a gap could hide writes made by a different owner.
func (l *Lease) Verify() error {
	if l == nil {
		return ErrLost
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return ErrLost
	}
	open, err := l.file.Stat()
	if err != nil {
		return errors.Join(ErrLost, err)
	}
	named, err := os.Stat(l.path)
	if err != nil {
		return errors.Join(ErrLost, err)
	}
	if !os.SameFile(l.identity, open) || !os.SameFile(open, named) {
		return ErrLost
	}
	return nil
}

func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
