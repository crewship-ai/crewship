package pipeline

import (
	"database/sql"
	"errors"
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
			SELECT 'gate-'||pipeline_run_id,workspace_id,'chat','approval','approval:'||token,'waitpoint',token FROM pipeline_waitpoints WHERE token NOT LIKE 'wp_%'`)
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
	// The production startup pass has a time budget. A slower host can need
	// subsequent sweeps; reaching the pass cap with a backlog is a failure.
	finish := func(store *SQLWaitpointStore, want int) {
		t.Helper()
		for range 8 {
			var remaining int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM pipeline_waitpoints WHERE "+legacyWaitpointPredicate).Scan(&remaining); err != nil {
				t.Fatal(err)
			}
			if remaining == 0 {
				break
			}
			store.migrateLegacyTokens()
		}
		assertCounts(want)
	}
	seed(1, 4096)
	start := time.Now()
	store := NewSQLWaitpointStore(db)
	t.Cleanup(store.Close)
	t.Logf("startup: %s for 4096 waitpoints and 120000 unrelated deliveries", time.Since(start))
	finish(store, 4096)

	seed(4097, 5120)
	start = time.Now()
	store.sweepOnce()
	store.migrateLegacyTokens()
	t.Logf("sweep: %s for 1024 added waitpoints; 5120 in total", time.Since(start))
	finish(store, 5120)

	start = time.Now()
	store.Close()
	t.Logf("shutdown: %s", time.Since(start))
	var check sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT max(entity_id) FROM audit_logs WHERE entity_type = 'waitpoint'").Scan(&check); err != nil {
		t.Fatal(err)
	}
}

// Keep one row in the backlog across startup independently of machine speed.
// Stop the first store before removing the fixture fault and starting its successor.
func TestWaitpointCallbackPartialMigrationRestart(t *testing.T) {
	ctx := t.Context()
	db := testutil.MigratedSQLDB(t)
	const ws = "ws-restart"
	const old = "ea9876543210123456789abcdefabcde"
	_, err := db.ExecContext(ctx, `INSERT INTO workspaces(id,name,slug) VALUES('ws-restart','Restart','restart');
		INSERT INTO users(id,email,full_name) VALUES('operator','operator@example.test','Manager');
		INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,timeout_at)
		VALUES('legacy-other','ws-restart','other','gate','approval','2099-01-01'),(?,'ws-restart','partial','gate','approval','2099-01-01')`, old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO inbox_items(id,workspace_id,kind,source_id,title)
		SELECT 'ibx_ws-restart_waitpoint_'||token,workspace_id,'waitpoint',token,'Approve' FROM pipeline_waitpoints;
		CREATE TRIGGER defer_one_waitpoint BEFORE UPDATE OF token ON pipeline_waitpoints
		WHEN OLD.pipeline_run_id='partial' BEGIN SELECT RAISE(ABORT,'defer fixture row'); END`); err != nil {
		t.Fatal(err)
	}
	store := NewSQLWaitpointStore(db)
	t.Cleanup(store.Close)
	var legacy int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM pipeline_waitpoints WHERE "+legacyWaitpointPredicate).Scan(&legacy); err != nil || legacy != 1 {
		t.Fatalf("partial migration backlog=%d (%v)", legacy, err)
	}
	// Exercise a manager issuing a URL and then deciding the same pending
	// item through the authenticated door while its migration is deferred.
	if _, _, err = store.MintCallbackToken(ctx, ws, old, "operator"); err != nil {
		t.Fatal(err)
	}
	actor := WaitpointDecider{Kind: DeciderUser, ID: "operator"}
	if err = store.Decide(ctx, ws, old, true, actor, "approved before restart"); err != nil {
		t.Fatal(err)
	}
	if err = store.Decide(ctx, ws, old, false, actor, "second decision"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second decision: %v", err)
	}
	store.Close()
	if _, err = db.ExecContext(ctx, "DROP TRIGGER defer_one_waitpoint"); err != nil {
		t.Fatal(err)
	}
	next := NewSQLWaitpointStore(db)
	t.Cleanup(next.Close)
	id := LegacyWaitpointPublicID(ws, old)
	var count int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM pipeline_waitpoints w JOIN inbox_items i ON w.workspace_id=i.workspace_id AND w.token=i.source_id
		WHERE w.token=? AND w.status='approved' AND w.decided_by_user_id='operator' AND w.decision_payload='approved before restart'
		AND i.state='resolved' AND i.resolved_action='approved'`, id).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("restored approval and resolved card=%d (%v)", count, err)
	}
	if _, _, err = next.DecideCallback(ctx, old, true, ""); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("old URL resolved after migration: %v", err)
	}
	if err = next.Decide(ctx, ws, id, false, actor, "after restart"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("decision changed after restart: %v", err)
	}
	assertNoPlaintext(t, db, old)
}
