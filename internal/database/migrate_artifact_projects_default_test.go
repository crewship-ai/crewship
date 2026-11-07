package database

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/featureflags"
)

// artifactProjectsDefaultOnVersion turns the artifact_projects flag on for
// every database whose row is still the untouched migration default (#4994).
const artifactProjectsDefaultOnVersion = 20261105094324

// TestArtifactProjectsDefaultOnRespectsExplicitChoices is the resolution table
// for #4994: after the upgrade a workspace that never chose gets Projects,
// while every deliberate off — a workspace override, an admin toggle of the
// instance default, an admin-recreated or deleted flag — still wins.
func TestArtifactProjectsDefaultOnRespectsExplicitChoices(t *testing.T) {
	const (
		wsInherit = "ws-inherit"
		wsOff     = "ws-off"
		wsOn      = "ws-on"
	)
	type want map[string]bool
	for _, tc := range []struct {
		name string
		// before runs against the pre-upgrade schema: the row exactly as the
		// 20261027175100 migration seeded it (enabled=0, created_at=updated_at
		// in SQLite datetime format, id ff_artifact_projects).
		before string
		want   want
	}{
		{
			name: "migration default, nobody chose",
			want: want{wsInherit: true, wsOff: false, wsOn: true},
		},
		{
			// FeatureFlagHandler.Update writes updated_at in RFC 3339
			// (internal/api/helpers.go newUpdate), never equal to the seed.
			name:   "admin explicitly turned the instance default off",
			before: `UPDATE feature_flags SET enabled=0, updated_at='2026-11-01T10:00:00Z' WHERE key='artifact_projects'`,
			want:   want{wsInherit: false, wsOff: false, wsOn: true},
		},
		{
			name:   "admin edited the flag without enabling it",
			before: `UPDATE feature_flags SET description='ours', updated_at='2026-11-01T10:00:00Z' WHERE key='artifact_projects'`,
			want:   want{wsInherit: false, wsOff: false, wsOn: true},
		},
		{
			// FeatureFlagHandler.Create: a random id and created_at=updated_at.
			name: "admin deleted and recreated the flag off",
			before: `DELETE FROM feature_flags WHERE key='artifact_projects';
				INSERT INTO feature_flags(id,key,description,enabled,created_at,updated_at)
				 VALUES('admin-made','artifact_projects','recreated',0,'2026-11-01T10:00:00Z','2026-11-01T10:00:00Z')`,
			want: want{wsInherit: false, wsOff: false, wsOn: true},
		},
		{
			name:   "admin deleted the flag",
			before: `DELETE FROM feature_flags WHERE key='artifact_projects'`,
			want:   want{wsInherit: false, wsOff: false, wsOn: false},
		},
		{
			name:   "instance already on",
			before: `UPDATE feature_flags SET enabled=1, updated_at='2026-11-01T10:00:00Z' WHERE key='artifact_projects'`,
			want:   want{wsInherit: true, wsOff: false, wsOn: true},
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
			var baseline []migration
			for _, m := range migrations {
				if m.version < artifactProjectsDefaultOnVersion {
					baseline = append(baseline, m)
				}
			}
			if err = applyRegistry(ctx, db.DB, baseline, logger); err != nil {
				t.Fatal(err)
			}
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			for _, ws := range []string{wsInherit, wsOff, wsOn} {
				exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,?,?)`, ws, ws, ws)
			}
			if tc.before != "" {
				exec(tc.before)
			}
			// Workspace overrides attach to whatever row now carries the key.
			exec(`INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled)
				SELECT 'o-off',id,?,0 FROM feature_flags WHERE key='artifact_projects'`, wsOff)
			exec(`INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled)
				SELECT 'o-on',id,?,1 FROM feature_flags WHERE key='artifact_projects'`, wsOn)

			if err = applyRegistry(ctx, db.DB, migrations, logger); err != nil {
				t.Fatal(err)
			}
			// Idempotent: the statement again changes nothing.
			for _, m := range migrations {
				if m.version == artifactProjectsDefaultOnVersion {
					exec(m.sql)
				}
			}
			for ws, expected := range tc.want {
				got, err := featureflags.IsEnabled(ctx, db.DB, ws, "artifact_projects")
				if err != nil {
					t.Fatal(err)
				}
				if got != expected {
					t.Errorf("%s: artifact_projects=%v, want %v", ws, got, expected)
				}
			}
		})
	}
}

// TestArtifactProjectsOnForFreshInstallWorkspace: a new server and a new
// workspace get Projects with no flag change at all.
func TestArtifactProjectsOnForFreshInstallWorkspace(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	if err = Migrate(ctx, db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO workspaces(id,name,slug) VALUES('fresh','Fresh','fresh')`); err != nil {
		t.Fatal(err)
	}
	got, err := featureflags.IsEnabled(ctx, db.DB, "fresh", "artifact_projects")
	if err != nil || !got {
		t.Fatalf("fresh workspace artifact_projects=%v err=%v, want on", got, err)
	}
	// Only Projects becomes a default; the other Artifact surfaces stay opt-in.
	for _, key := range []string{"artifacts_design", "artifact_slides", "artifact_dashboard_data", "gridcraft_editor"} {
		on, err := featureflags.IsEnabled(ctx, db.DB, "fresh", key)
		if err != nil || on {
			t.Errorf("%s=%v err=%v, want off", key, on, err)
		}
	}
}
