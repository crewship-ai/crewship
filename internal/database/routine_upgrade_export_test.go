package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
)

// Exported only to the external test package, which can use the real pipeline
// store without introducing an import cycle into database.
func RoutineUpgradeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(ctx, db, 20260908223756, logger); err != nil {
		t.Fatal(err)
	}
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO workspaces(id,name,slug) VALUES('upgrade-ws','Upgrade','upgrade')`)
	for _, id := range []string{"one", "two"} {
		must(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version) VALUES(?,'upgrade-ws',?,?,'{"steps":[]}','legacy',?)`, id, id, id, len(id))
	}
	must(`UPDATE pipelines SET head_version=7 WHERE id='two'`)
	for _, status := range []string{"pending", "cancelled"} {
		must(`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,fire_at,status) VALUES(?,'upgrade-ws','one','one','2099-01-01T00:00:00Z',?)`, status, status)
	}
	for _, status := range []string{"pending", "approved"} {
		must(`INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,status,timeout_at,decision_payload) VALUES(?,'upgrade-ws','legacy-run','gate','approval',?,'2099-01-01T00:00:00Z','{"legacy":true}')`, status, status)
	}
	if err := Migrate(ctx, db, logger); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM pipelines WHERE id IN ('one','two') AND publication_revision=1`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("base revisions: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pending_runs WHERE id IN ('pending','cancelled') AND pinned_version IS NULL AND status=id`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy start semantics: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pipeline_waitpoints WHERE token IN ('pending','approved') AND decision_form_json='' AND status=token AND decision_payload='{"legacy":true}'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy decisions: %d %v", count, err)
	}
	must(`UPDATE pipelines SET description='edited after upgrade' WHERE id='one'`)
	if err := db.QueryRow(`SELECT publication_revision FROM pipelines WHERE id='one'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("revision trigger: %d %v", count, err)
	}
	return db
}
