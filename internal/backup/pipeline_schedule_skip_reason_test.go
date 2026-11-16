package backup

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestScheduleSkipReasonBackupRoundtripAndLegacyOmission(t *testing.T) {
	source := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('skip-w','Skip','skip')`,
		`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash) VALUES('skip-p','skip-w','Skip','skip','{}','hash')`,
	} {
		if _, err := source.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	reasons := []string{"capacity", "catchup", "inactive", "busy", ""}
	for _, reason := range reasons {
		if _, err := source.Exec(`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,last_status,last_skip_reason,last_run_at) VALUES(?,'skip-w','Skip','skip-p','* * * * *','SKIPPED',?,'2026-11-01T10:00:00Z')`, "skip-"+reason, reason); err != nil {
			t.Fatal(err)
		}
	}
	dump, err := DumpWorkspace(t.Context(), source, "skip-w")
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.MigratedSQLDB(t)
	if err := RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	for _, want := range reasons {
		var got, at string
		if err := target.QueryRow(`SELECT last_skip_reason,last_run_at FROM pipeline_schedules WHERE id=?`, "skip-"+want).Scan(&got, &at); err != nil || got != want || at != "2026-11-01T10:00:00Z" {
			t.Errorf("roundtrip reason %q: got=%q at=%q err=%v", want, got, at, err)
		}
	}
	// Model an older bundle by omitting the additive field. Historical causes
	// cannot be reconstructed from status, lifecycle or missed-count telemetry.
	for _, row := range dump.Tables["pipeline_schedules"] {
		delete(row, "last_skip_reason")
	}
	legacyTarget := testutil.MigratedSQLDB(t)
	if err := RestoreDump(t.Context(), legacyTarget, dump); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := legacyTarget.QueryRow(`SELECT count(*) FROM pipeline_schedules WHERE last_skip_reason='' AND last_status='SKIPPED'`).Scan(&count); err != nil || count != len(reasons) {
		t.Fatalf("legacy restore defaults count=%d want%d: %v", count, len(reasons), err)
	}
}
