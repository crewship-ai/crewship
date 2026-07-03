package memory

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// writeFileDurable persists content at path with crash-durable, atomic
// semantics: write to a sibling tempfile, fsync it, atomically rename it
// over the target, then fsync the parent directory so the rename's
// directory entry is on stable storage before we return. A reader
// concurrent with the write sees either the whole old file or the whole
// new one, never a torn/truncated intermediate.
//
// This is the durability half of fix 2a — "a memory.write that returns
// success must be durably recorded" — grounded in the Write-Ahead
// Logging discipline (Mohan et al., ARIES 1992): never ACK a write until
// it is fsync'd. The plain os.WriteFile it replaces only reaches the page
// cache and truncates in place, so a crash after "ok" loses the write and
// an interrupted write corrupts the file. Extracted from WriteFile's
// internal temp+fsync+rename core so both the agent-facing memory.write
// dispatcher and the higher-level WriteFile share one durability path.
//
// On any failure the target is left untouched and the tempfile is
// cleaned up, so the caller can safely surface an is_error result
// without having half-persisted anything.
func writeFileDurable(path string, content []byte, perm os.FileMode) (err error) {
	var randBuf [8]byte
	if _, rerr := rand.Read(randBuf[:]); rerr != nil {
		return fmt.Errorf("rand for tempname: %w", rerr)
	}
	tmpPath := path + ".tmp." + hex.EncodeToString(randBuf[:])

	// O_EXCL so we never adopt a crashed writer's leftover tempfile.
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("open tempfile: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("write tempfile: %w", err)
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("fsync tempfile: %w", err)
	}
	if err = f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close tempfile: %w", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("atomic rename: %w", err)
	}
	// fsync the parent dir so the rename's directory-entry update is
	// durable — on ext4/xfs a crash between rename and the next flush can
	// otherwise revive the prior entry. Data file is already at `path`, so
	// we do not roll back on dir-fsync failure.
	dir, openErr := os.Open(filepath.Dir(path))
	if openErr != nil {
		return fmt.Errorf("open parent dir for fsync: %w", openErr)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return fmt.Errorf("fsync parent dir: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close parent dir: %w", closeErr)
	}
	return nil
}
