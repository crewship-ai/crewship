package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
)

var acceptanceNow = time.Date(2026, 9, 22, 12, 7, 30, 0, time.UTC)

const acceptanceDue = "2026-09-22T12:00:00Z"

func dueFixture(t *testing.T) (*sql.DB, *work.Store) {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO workspaces (id,name,slug) VALUES ('ws1','Test','test')`,
		`INSERT INTO crews (id,workspace_id,name,slug) VALUES ('crew1','ws1','Test','test')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "* * * * *", "keep this prompt", true)
	setNextRun(t, db, "a1", acceptanceDue)
	return db, work.NewStore(db).WithClock(func() time.Time { return acceptanceNow })
}

func acceptDue(db *sql.DB, store *work.Store, workspace string, limits work.IngressLimits) (work.Receipt, error) {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return work.Receipt{}, err
	}
	defer tx.Rollback()
	r, err := AcceptDueTx(ctx, tx, store, workspace, "a1", acceptanceNow, limits)
	if err != nil {
		return work.Receipt{}, err
	}
	return r, tx.Commit()
}

func TestAcceptDueTx_AtomicCursorAndImmutableInput(t *testing.T) {
	db, store := dueFixture(t)
	r, err := acceptDue(db, store, "ws1", work.IngressLimits{})
	if err != nil {
		t.Fatal(err)
	}
	it, err := store.Get(context.Background(), r.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	var input ScheduledInput
	if err := json.Unmarshal([]byte(it.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if input.Version != 1 || input.Prompt != "keep this prompt" || input.Occurrence != acceptanceDue || input.AgentID != "a1" {
		t.Fatalf("incorrect immutable input: %+v", input)
	}
	if it.Source != work.SourceSchedule || it.Class != work.ClassBackground || it.State != work.StateQueued || it.Attempts != 0 || it.SessionID == "" || it.DeadlineAt != nil {
		t.Fatalf("unexpected work: %+v", it)
	}
	last, next := agentSchedule(t, db, "a1")
	if last.Valid || next.String != "2026-09-22T12:08:00Z" {
		t.Fatalf("last=%v next=%v", last, next)
	}
	if _, err := acceptDue(db, store, "ws1", work.IngressLimits{}); !errors.Is(err, ErrScheduleNotDue) {
		t.Fatalf("duplicate tick: %v", err)
	}
	// A stale cursor cannot change the accepted prompt or create another work,
	// even when the queue is full (duplicate lookup precedes admission checks).
	setNextRun(t, db, "a1", acceptanceDue)
	if _, err := db.Exec(`UPDATE agents SET schedule_prompt='changed' WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	dup, err := acceptDue(db, store, "ws1", work.IngressLimits{WorkspaceNonTerminal: 1, WorkspaceRawBytes: 1})
	if err != nil || !dup.Duplicate || dup.WorkID != r.WorkID {
		t.Fatalf("duplicate=%+v err=%v", dup, err)
	}
	original, err := store.Get(context.Background(), r.WorkID)
	if err != nil || original.InputJSON != it.InputJSON {
		t.Fatalf("input changed: %v", err)
	}
}

func TestAcceptDueTx_RollbackIncludesWorkAndCursor(t *testing.T) {
	db, store := dueFixture(t)
	// Fail the final statement after AcceptTx inserted work and its event.
	if _, err := db.Exec(`CREATE TRIGGER reject_cursor BEFORE UPDATE OF schedule_next_run ON agents BEGIN SELECT RAISE(ABORT, 'cursor failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptDue(db, store, "ws1", work.IngressLimits{}); err == nil {
		t.Fatal("expected cursor failure")
	}
	for _, table := range []string{"work_items", "work_events"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s has %d rows after rollback", table, count)
		}
	}
	_, next := agentSchedule(t, db, "a1")
	if next.String != acceptanceDue {
		t.Fatalf("cursor advanced: %v", next)
	}
}

func TestAcceptDueTx_ConcurrentTicksAcceptOnce(t *testing.T) {
	db, store := dueFixture(t)
	start := make(chan struct{})
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := acceptDue(db, store, "ws1", work.IngressLimits{})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrScheduleNotDue) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d ticks", accepted)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("work count=%d", count)
	}
}

func TestAcceptDueTx_RefusalDoesNotAdvance(t *testing.T) {
	for _, tc := range []struct {
		name, change, workspace string
		limits                  work.IngressLimits
		want                    error
	}{
		{name: "workspace isolation", workspace: "other", want: ErrScheduleUnavailable},
		{name: "disabled", change: `UPDATE agents SET schedule_enabled=0 WHERE id='a1'`, workspace: "ws1", want: ErrScheduleUnavailable},
		{name: "deleted", change: `UPDATE agents SET deleted_at='2026-09-22' WHERE id='a1'`, workspace: "ws1", want: ErrScheduleUnavailable},
		{name: "deleted crew", change: `UPDATE crews SET deleted_at='2026-09-22' WHERE id='crew1'`, workspace: "ws1", want: ErrScheduleUnavailable},
		{name: "deleted workspace", change: `UPDATE workspaces SET deleted_at='2026-09-22' WHERE id='ws1'`, workspace: "ws1", want: ErrScheduleUnavailable},
		{name: "bytes", workspace: "ws1", limits: work.IngressLimits{WorkspaceRawBytes: 1}, want: work.ErrWorkspaceBytesFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, store := dueFixture(t)
			if tc.change != "" {
				if _, err := db.Exec(tc.change); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := acceptDue(db, store, tc.workspace, tc.limits); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			_, next := agentSchedule(t, db, "a1")
			if next.String != acceptanceDue {
				t.Fatalf("cursor advanced: %v", next)
			}
		})
	}
}

func TestAcceptDueTx_LegacyReservationBlocksNewOwner(t *testing.T) {
	db, store := dueFixture(t)
	key := pipeline.ScheduledFireIdempotencyKey("agent-sched", "a1", acceptanceDue)
	if _, err := db.Exec(`INSERT INTO pipeline_run_idempotency (workspace_id,pipeline_id,idempotency_key,run_id,expires_at) VALUES ('ws1','a1',?,'old-run','2026-09-23T12:00:00Z')`, key); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptDue(db, store, "ws1", work.IngressLimits{}); !errors.Is(err, ErrLegacyOccurrence) {
		t.Fatalf("legacy owner ignored: %v", err)
	}
	_, next := agentSchedule(t, db, "a1")
	if next.String != acceptanceDue {
		t.Fatal("legacy ownership advanced cursor")
	}
}

func TestAcceptDueTx_UniqueIndexRejectsBypass(t *testing.T) {
	db, store := dueFixture(t)
	r, err := acceptDue(db, store, "ws1", work.IngressLimits{})
	if err != nil {
		t.Fatal(err)
	}
	it, err := store.Get(context.Background(), r.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = store.AcceptTx(context.Background(), tx, work.AcceptRequest{WorkspaceID: "ws1", Source: work.SourceSchedule, SourceRef: it.SourceRef, AgentID: "a1", DomainKind: work.DomainAgentRun, Class: work.ClassBackground})
	if err == nil {
		t.Fatal("unique occurrence index allowed a second automatic work")
	}
}

func TestAcceptDueTx_BacklogLimitsPreserveUnacceptedOccurrence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits work.IngressLimits
		want   error
	}{
		{"per agent", work.IngressLimits{EndpointNonTerminal: 1}, work.ErrEndpointFull},
		{"workspace", work.IngressLimits{WorkspaceNonTerminal: 1}, work.ErrWorkspaceFull},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, store := dueFixture(t)
			if _, err := acceptDue(db, store, "ws1", work.IngressLimits{}); err != nil {
				t.Fatal(err)
			}
			const second = "2026-09-22T12:01:00Z"
			setNextRun(t, db, "a1", second)
			if _, err := acceptDue(db, store, "ws1", tc.limits); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
			_, next := agentSchedule(t, db, "a1")
			if next.String != second {
				t.Fatalf("rejected occurrence consumed: %v", next)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("full queue accepted new work: %d", n)
			}
		})
	}
}
