package database

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/featureflags"
)

// artifactInteractiveFlagVersion seeds the default-off artifact_interactive
// flag (#4720).
const artifactInteractiveFlagVersion = 20261107142708

// TestArtifactInteractiveFlagAppliesOnMigratedDatabase upgrades a database
// that already carries every earlier migration — the state of any running
// installation — and checks the flag arrives off, keeps an admin's existing
// row, and re-running the statement changes nothing.
func TestArtifactInteractiveFlagAppliesOnMigratedDatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before string
		want   bool
	}{
		{name: "fresh upgrade seeds the flag off", want: false},
		{
			name: "admin-created row is preserved",
			before: `INSERT INTO feature_flags(id,key,description,enabled,created_at,updated_at)
				 VALUES('admin-made','artifact_interactive','ours',1,'2026-11-01T10:00:00Z','2026-11-01T10:00:00Z')`,
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := Open(filepath.Join(t.TempDir(), "flags.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ctx := t.Context()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err = ensureMigrationsTable(ctx, db.DB); err != nil {
				t.Fatal(err)
			}
			var found bool
			var baseline []migration
			for _, m := range migrations {
				if m.version == artifactInteractiveFlagVersion {
					found = true
					continue
				}
				if m.version < artifactInteractiveFlagVersion {
					baseline = append(baseline, m)
				}
			}
			if !found {
				t.Fatalf("migration v%d not in registry", artifactInteractiveFlagVersion)
			}
			if err = applyRegistry(ctx, db.DB, baseline, logger); err != nil {
				t.Fatal(err)
			}
			exec := func(q string) {
				t.Helper()
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			const ws = "ws-1"
			exec(`INSERT INTO workspaces(id,name,slug) VALUES('ws-1','ws-1','ws-1')`)
			if tc.before != "" {
				exec(tc.before)
			}
			if err = applyRegistry(ctx, db.DB, migrations, logger); err != nil {
				t.Fatal(err)
			}
			var applied int
			if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM _migrations WHERE version = ?`, artifactInteractiveFlagVersion).Scan(&applied); err != nil {
				t.Fatal(err)
			}
			if applied != 1 {
				t.Fatalf("v%d recorded %d times, want 1", artifactInteractiveFlagVersion, applied)
			}
			for _, m := range migrations {
				if m.version == artifactInteractiveFlagVersion {
					exec(m.sql)
				}
			}
			var rows int
			if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM feature_flags WHERE key='artifact_interactive'`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 1 {
				t.Fatalf("artifact_interactive rows = %d, want 1", rows)
			}
			got, err := featureflags.IsEnabled(ctx, db.DB, ws, "artifact_interactive")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("artifact_interactive enabled = %v, want %v", got, tc.want)
			}
		})
	}
}
