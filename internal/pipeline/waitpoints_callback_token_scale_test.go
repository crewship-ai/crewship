package pipeline

import (
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestWaitpointCallbackSyntheticMigrationLifecycle(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	ctx := t.Context()
	execSQL := func(stmt string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(`INSERT INTO workspaces(id,name,slug) VALUES('ws-scale','Scale','scale')`)
	execSQL(`WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n < 120000)
		INSERT INTO notification_deliveries(id,workspace_id,channel_id,category,dedup_key,source_kind,source_id)
		SELECT 'history-'||n,'ws-scale','chat','routine','routine:'||n,'routine',CAST(n AS TEXT) FROM seq`)
	seed := func(first, last int) {
		execSQL(`WITH RECURSIVE seq(n) AS (VALUES(?) UNION ALL SELECT n+1 FROM seq WHERE n < ?)
			INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,timeout_at)
			SELECT printf('%032x', n),'ws-scale','run-'||n,'approve','approval','2099-01-01T00:00:00Z' FROM seq`, first, last)
		execSQL(`INSERT INTO inbox_items(id,workspace_id,kind,source_id,title)
			SELECT 'ibx_ws-scale_waitpoint_'||token,workspace_id,'waitpoint',token,'pending' FROM pipeline_waitpoints WHERE token NOT LIKE 'wp_%'`)
		execSQL(`INSERT INTO notification_deliveries(id,workspace_id,channel_id,category,dedup_key,source_kind,source_id)
			SELECT 'gate-'||token,workspace_id,'chat','approval','approval:'||token,'waitpoint',token FROM pipeline_waitpoints WHERE token NOT LIKE 'wp_%'`)
	}
	assertCounts := func(want int) {
		t.Helper()
		var waitpoints, inbox, delivery int
		if err := db.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM pipeline_waitpoints WHERE token LIKE 'wp_%' AND callback_token_hash IS NULL AND status='pending'),
			       (SELECT count(*) FROM inbox_items JOIN pipeline_waitpoints ON source_id=token WHERE inbox_items.kind='waitpoint'),
			       (SELECT count(*) FROM notification_deliveries JOIN pipeline_waitpoints ON source_id=token WHERE source_kind='waitpoint')
		`).Scan(&waitpoints, &inbox, &delivery); err != nil {
			t.Fatal(err)
		}
		if waitpoints != want || inbox != want || delivery != want {
			t.Fatalf("preserved rows: waitpoints=%d inbox=%d deliveries=%d, want %d each", waitpoints, inbox, delivery, want)
		}
	}
	seed(1, 4096)
	start := time.Now()
	store := NewSQLWaitpointStore(db)
	t.Cleanup(store.Close)
	t.Logf("startup: %s for 4096 waitpoints and 120000 unrelated deliveries", time.Since(start))
	assertCounts(4096)

	seed(4097, 5120)
	start = time.Now()
	store.sweepOnce()
	store.migrateLegacyTokens()
	t.Logf("sweep: %s for 1024 added waitpoints; 5120 in total", time.Since(start))
	assertCounts(5120)

	start = time.Now()
	store.Close()
	t.Logf("shutdown: %s", time.Since(start))
	var check sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT max(entity_id) FROM audit_logs WHERE entity_type = 'waitpoint'").Scan(&check); err != nil {
		t.Fatal(err)
	}
}
