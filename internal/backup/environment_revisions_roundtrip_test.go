package backup_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestEnvironmentRevisionsRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	for _, crew := range []string{"revision-crew", "sibling-crew"} {
		if _, err := source.ExecContext(ctx, `INSERT INTO crews(id,workspace_id,name,slug) VALUES(?,?,?,?)`, crew, workspace, crew, crew); err != nil {
			t.Fatal(err)
		}
		if _, err := source.ExecContext(ctx, `INSERT INTO environment_revisions(id,workspace_id,crew_id,definition_hash,build_hash,image_id,toolchain_json,created_at) VALUES(?,?,?,'definition','build','sha256:artifact','{"schema_version":1,"status":"recorded","tools":[]}','2026-10-03T00:00:00Z')`, crew+"-revision", workspace, crew); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{"workspace", "crew"} {
		t.Run(scope, func(t *testing.T) {
			var dump *backup.DBDump
			var err error
			want := 2
			if scope == "crew" {
				dump, err = backup.DumpCrew(ctx, source, "revision-crew")
				want = 1
			} else {
				dump, err = backup.DumpWorkspace(ctx, source, workspace)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := len(dump.Tables["environment_revisions"]); got != want {
				t.Fatalf("dumped %d revisions, want %d", got, want)
			}
			target := openMigratedDB(t)
			if err := backup.RestoreDump(ctx, target, dump); err != nil {
				t.Fatal(err)
			}
			var image, inventory string
			if err := target.QueryRowContext(ctx, `SELECT image_id,toolchain_json FROM environment_revisions WHERE id='revision-crew-revision'`).Scan(&image, &inventory); err != nil {
				t.Fatal(err)
			}
			if image != "sha256:artifact" || inventory != `{"schema_version":1,"status":"recorded","tools":[]}` {
				t.Fatalf("restored provenance changed: %q %q", image, inventory)
			}
			if _, err := target.ExecContext(ctx, `UPDATE environment_revisions SET image_id='other' WHERE id='revision-crew-revision'`); err == nil {
				t.Fatal("restored revision lost immutability")
			}
		})
	}
}
