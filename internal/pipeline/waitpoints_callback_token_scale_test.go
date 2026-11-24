package pipeline

import (
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func migrateWaitpointFixture(t testing.TB, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := database.ConvertWaitpointIdentities(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitpointCallbackSyntheticMigrationLifecycle(t *testing.T) {
	measureWaitpointConversion(t, 4096)
}

func BenchmarkWaitpointBootConversion(b *testing.B) {
	for _, size := range []int{10, 100, 4096, 50000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			for range b.N {
				measureWaitpointConversion(b, size)
			}
		})
	}
}

func measureWaitpointConversion(t testing.TB, count int) {
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
	seed(1, count)
	start := time.Now()
	migrateWaitpointFixture(t, db)
	elapsed := time.Since(start)
	t.Logf("converted %d waitpoints in %s", count, elapsed)
	if b, ok := t.(*testing.B); ok {
		b.ReportMetric(float64(elapsed.Nanoseconds())/1e6, "ms/conversion")
	}
	assertCounts(count)
}

func TestWaitpointLegacyDecisionReceipts(t *testing.T) {
	for _, outcome := range []string{"approved", "denied", "timed_out", "refused"} {
		t.Run(outcome, func(t *testing.T) {
			db := testutil.MigratedSQLDB(t)
			const old = "a8c928f787df42e69d62e4c5f0321a63"
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('ws','W','w');
				INSERT INTO users(id,email,full_name) VALUES('user','user@example.test','User');
				INSERT INTO pipeline_waitpoints(token,workspace_id,pipeline_run_id,step_id,kind,timeout_at) VALUES(?,'ws','run','review','approval','2099-01-01');
				INSERT INTO inbox_items(id,workspace_id,kind,source_id,title) VALUES('card','ws','waitpoint',?,'Review')`, old, old); err != nil {
				t.Fatal(err)
			}
			migrateWaitpointFixture(t, db)
			store := NewSQLWaitpointStore(db)
			defer store.Close()
			id := LegacyWaitpointPublicID("ws", old)
			switch outcome {
			case "timed_out":
				if _, err := db.Exec("UPDATE pipeline_waitpoints SET timeout_at='2000-01-01' WHERE token=?", id); err != nil {
					t.Fatal(err)
				}
				store.sweepOnce()
			case "refused":
				err := store.Decide(t.Context(), "ws", id, true, WaitpointDecider{Kind: DeciderAgent, ID: "agent"}, "")
				if !errors.Is(err, ErrDeciderNotAllowed) {
					t.Fatalf("refusal: %v", err)
				}
				var n int
				if err := db.QueryRow("SELECT count(*) FROM audit_logs WHERE entity_id=?", old).Scan(&n); err != nil || n != 0 {
					t.Fatalf("legacy id in refusal receipt (%v)", err)
				}
			default:
				if err := store.Decide(t.Context(), "ws", id, outcome == "approved", WaitpointDecider{Kind: DeciderUser, ID: "user"}, "reviewed"); err != nil {
					t.Fatal(err)
				}
			}
			want := outcome
			if outcome == "refused" {
				want = "pending"
			}
			var status string
			if err := db.QueryRow("SELECT status FROM pipeline_waitpoints WHERE token=?", LegacyWaitpointPublicID("ws", old)).Scan(&status); err != nil || status != want {
				t.Fatalf("status=%q (%v)", status, err)
			}
			assertNoPlaintext(t, db, old)
		})
	}
}
