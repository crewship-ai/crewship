// Package migratedtemplate builds the same complete, private SQLite fixture in
// either the test process or its normal-instrumentation helper. It is not a
// production migration entrypoint and refuses an existing database.
package migratedtemplate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/database"
)

// Build opens a fresh database with the fixture's existing NORMAL setting,
// runs every ordinary migration, checks the WAL checkpoint and closes it before
// publication. Consumers still choose their own connection-pool options.
func Build(ctx context.Context, path string) (err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "?\x00#") {
		return fmt.Errorf("fixture output must be a clean absolute file path")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, e := os.Lstat(path + suffix); e == nil {
			return fmt.Errorf("fixture sidecar already exists: %s", path+suffix)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create fresh fixture: %w", err)
	}
	if err = file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	complete := false
	defer func() {
		if !complete {
			for _, s := range []string{"", "-wal", "-shm"} {
				_ = os.Remove(path + s)
			}
		}
	}()
	db, err := database.Open("file:"+path, database.WithSynchronous(database.SynchronousNormal))
	if err != nil {
		return err
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = database.Migrate(ctx, db.DB, logger); err != nil {
		return err
	}
	var busy, walPages, checkpointed int
	if err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &walPages, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint template WAL: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint template WAL: busy (log=%d pages, checkpointed=%d)", walPages, checkpointed)
	}
	if err = db.Close(); err != nil {
		return err
	}
	db = nil
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove template %s: %w", suffix, err)
		}
	}
	complete = true
	return nil
}
