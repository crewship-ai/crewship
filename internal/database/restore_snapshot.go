package database

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Snapshot describes one pre-migration snapshot on disk (written by
// SnapshotBeforeMigrate as "<db>.pre-migrate-v<from>-to-v<to>-<ts>.bak").
type Snapshot struct {
	Path        string    // absolute path to the .bak file
	Name        string    // base filename
	FromVersion int       // schema version the snapshot was taken AT
	ToVersion   int       // version the pending migration would have moved to
	TakenAt     time.Time // parsed from the filename timestamp (UTC)
	Size        int64     // bytes
}

// snapshotNameRE parses the SnapshotBeforeMigrate filename shape. The
// timestamp is goreleaser-free RFC3339-ish: 20060102T150405Z.
var snapshotNameRE = regexp.MustCompile(`\.pre-migrate-v(\d+)-to-v(\d+)-(\d{8}T\d{6}Z)\.bak$`)

// ListSnapshots returns the pre-migration snapshots that sit next to dbPath,
// newest first (by the timestamp encoded in the name, then mtime). Only files
// matching this DB's "<base>.pre-migrate-…-.bak" shape are returned, so an
// unrelated .bak in the same directory is never offered as a restore target.
func ListSnapshots(dbPath string) ([]Snapshot, error) {
	dir := filepath.Dir(dbPath)
	prefix := filepath.Base(dbPath) + ".pre-migrate-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Snapshot
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		m := snapshotNameRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		from, _ := strconv.Atoi(m[1])
		to, _ := strconv.Atoi(m[2])
		taken, _ := time.Parse("20060102T150405Z", m[3])
		var size int64
		if fi, err := e.Info(); err == nil {
			size = fi.Size()
		}
		out = append(out, Snapshot{
			Path:        filepath.Join(dir, name),
			Name:        name,
			FromVersion: from,
			ToVersion:   to,
			TakenAt:     taken,
			Size:        size,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TakenAt.After(out[j].TakenAt) })
	return out, nil
}

// RestoreSnapshot replaces the live database at dbPath with a pre-migration
// snapshot — the downgrade recovery path (pair with reinstalling the older
// binary; see the version-skew guard in Migrate). It:
//
//   - refuses any snapshotPath that isn't a pre-migrate snapshot belonging to
//     dbPath (so it can't clobber the DB with an arbitrary file),
//   - copies the CURRENT db aside to "<db>.before-restore-<ts>" first, so the
//     restore is itself reversible,
//   - removes the WAL/SHM sidecars so SQLite can't replay stale WAL frames
//     over the restored file,
//   - then copies the snapshot into place.
//
// The caller MUST ensure crewshipd is not running against dbPath (a live
// server holds the DB open and would see a torn file); the CLI wrapper checks
// for a running local server before calling this.
func RestoreSnapshot(dbPath, snapshotPath string) error {
	// Safety: the snapshot must be one of THIS db's pre-migrate snapshots.
	valid := false
	if snaps, err := ListSnapshots(dbPath); err == nil {
		abs, _ := filepath.Abs(snapshotPath)
		for _, s := range snaps {
			if sabs, _ := filepath.Abs(s.Path); sabs == abs {
				valid = true
				break
			}
		}
	}
	if !valid {
		return fmt.Errorf("%q is not a pre-migration snapshot of %q (expected a \"%s.pre-migrate-*.bak\" file beside it)",
			snapshotPath, dbPath, filepath.Base(dbPath))
	}

	snapData, err := os.ReadFile(snapshotPath)
	if err != nil {
		return fmt.Errorf("read snapshot: %w", err)
	}
	// Cheap sanity: a SQLite file begins with "SQLite format 3\000".
	if len(snapData) < 16 || string(snapData[:15]) != "SQLite format 3" {
		return fmt.Errorf("snapshot %q is not a SQLite database", snapshotPath)
	}

	// Reversible: stash the current DB before overwriting it.
	if _, err := os.Stat(dbPath); err == nil {
		aside := dbPath + ".before-restore-" + time.Now().UTC().Format("20060102T150405Z")
		if cur, rerr := os.ReadFile(dbPath); rerr == nil {
			if werr := os.WriteFile(aside, cur, 0o600); werr != nil {
				return fmt.Errorf("stash current db before restore: %w", werr)
			}
		}
	}

	// Drop WAL/SHM so a stale WAL isn't replayed over the restored file.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s%s: %w", dbPath, suffix, err)
		}
	}

	if err := os.WriteFile(dbPath, snapData, 0o600); err != nil {
		return fmt.Errorf("write restored db: %w", err)
	}
	return nil
}
