package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// MigrateBefore must leave the database exactly as the release before the
// named migration did: every earlier migration applied, the named one and all
// later ones absent, and a following Migrate completing the upgrade.
func TestMigrateBeforeStopsAtTheNamedMigration(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tc := range []struct {
		name    string
		stop    string
		wantErr string
	}{
		{name: "middle", stop: migrations[len(migrations)/2].name},
		{name: "first", stop: migrations[0].name},
		{name: "unknown", stop: "no_such_migration", wantErr: `no migration is named "no_such_migration"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "before.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			err = MigrateBefore(ctx, db.DB, logger, tc.stop)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Exactly the boot-time migrations ordered before the stop: none
			// skipped, none after it. Post-deploy ones are deferred, as on a
			// real boot.
			wantCount, wantMax := 0, 0
			for _, m := range migrations {
				if m.name == tc.stop {
					break
				}
				if !m.postDeploy {
					wantCount++
					wantMax = m.version
				}
			}
			var applied, maxVersion int
			if err := db.QueryRow(`SELECT count(*), coalesce(max(version),0) FROM _migrations`).Scan(&applied, &maxVersion); err != nil {
				t.Fatal(err)
			}
			if applied != wantCount || maxVersion != wantMax {
				t.Fatalf("applied %d migrations up to version %d, want %d up to %d", applied, maxVersion, wantCount, wantMax)
			}
			if err := Migrate(ctx, db.DB, logger); err != nil {
				t.Fatalf("upgrade after MigrateBefore: %v", err)
			}
		})
	}
}
