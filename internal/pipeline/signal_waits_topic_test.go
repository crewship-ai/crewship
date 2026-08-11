package pipeline

// Topic-scoped signal delivery.
//
// Deliver() is keyed by run_id: the caller must already know WHICH run to
// wake. That is fine for an external system holding a run id it was handed,
// and useless for an internal event source — "an issue changed status" knows
// the workspace and the event, never the set of runs parked on it. Without a
// workspace+event lookup an internal producer would have to scan runs itself,
// which is the kind of second door this repo keeps closing.
//
// DeliverTopic is that lookup: every PENDING wait in a workspace matching an
// event_type flips to delivered in one claim, and the run ids come back so the
// caller can un-park each of them.
//
// The properties pinned here are the ones a fan-out delivery can get wrong:
//
//   - every run parked on the topic wakes, not just the oldest (Deliver's
//     LIMIT 1 semantics must NOT carry over);
//   - a run in another workspace never wakes — the workspace is a fence, not
//     a filter applied afterwards by the caller;
//   - a topic nobody waits on is an empty result and a nil error, not a
//     failure: an internal producer emits events whether or not anything is
//     listening, and an error there would make every unlistened event look
//     like a bug;
//   - two callers delivering the same topic at the same instant claim
//     disjoint sets — a run woken twice would resume its parked step twice,
//     which is the double-execution hazard the per-row 'pending' claim in
//     Deliver/ConsumeDelivered already guards against for the run-keyed path.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// openSignalWaitTestDB returns a fully-migrated DB (so the real
// pipeline_signal_waits schema and its indexes are exercised, including the
// topic index this feature adds) with a pool wide enough for the concurrency
// test to contend at the SQLite layer rather than at database/sql's.
func openSignalWaitTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	return db
}

// armWait is the arm half of a parked wait:event step, without needing a real
// run: DeliverTopic reads pipeline_signal_waits and nothing else.
func armWait(t *testing.T, s *SQLSignalWaitStore, workspaceID, runID, eventType string) {
	t.Helper()
	if err := s.Arm(context.Background(), workspaceID, runID, "gate", eventType); err != nil {
		t.Fatalf("arm %s/%s: %v", workspaceID, runID, err)
	}
}

func waitStatus(t *testing.T, db *sql.DB, runID string) (status string, payload sql.NullString) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT status, payload FROM pipeline_signal_waits WHERE run_id = ?`, runID,
	).Scan(&status, &payload); err != nil {
		t.Fatalf("read wait row for %s: %v", runID, err)
	}
	return status, payload
}

func TestSQLSignalWaitStore_DeliverTopic_WakesEveryRunOnTheTopic(t *testing.T) {
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	ctx := context.Background()

	armWait(t, s, "ws_a", "run_1", "mission.status_change")
	armWait(t, s, "ws_a", "run_2", "mission.status_change")
	armWait(t, s, "ws_a", "run_other_event", "mission.commented")

	woken, err := s.DeliverTopic(ctx, "ws_a", "mission.status_change", `{"status":"done"}`)
	if err != nil {
		t.Fatalf("deliver topic: %v", err)
	}
	sort.Strings(woken)
	if len(woken) != 2 || woken[0] != "run_1" || woken[1] != "run_2" {
		t.Fatalf("woken = %v, want [run_1 run_2] — a topic delivery must reach EVERY parked run, not just the oldest", woken)
	}

	for _, runID := range []string{"run_1", "run_2"} {
		status, payload := waitStatus(t, db, runID)
		if status != "delivered" {
			t.Errorf("%s status = %q, want delivered", runID, status)
		}
		if payload.String != `{"status":"done"}` {
			t.Errorf("%s payload = %q, want the delivered payload", runID, payload.String)
		}
	}
	// A different event_type in the same workspace is a different topic.
	if status, _ := waitStatus(t, db, "run_other_event"); status != "pending" {
		t.Errorf("run_other_event status = %q, want pending (wrong event_type must not be delivered)", status)
	}
}

func TestSQLSignalWaitStore_DeliverTopic_OtherWorkspaceDoesNotWake(t *testing.T) {
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	ctx := context.Background()

	armWait(t, s, "ws_a", "run_mine", "mission.status_change")
	armWait(t, s, "ws_b", "run_theirs", "mission.status_change")

	woken, err := s.DeliverTopic(ctx, "ws_a", "mission.status_change", "p")
	if err != nil {
		t.Fatalf("deliver topic: %v", err)
	}
	if len(woken) != 1 || woken[0] != "run_mine" {
		t.Fatalf("woken = %v, want [run_mine] — the workspace is a fence, a topic must not cross it", woken)
	}
	if status, payload := waitStatus(t, db, "run_theirs"); status != "pending" || payload.Valid {
		t.Errorf("foreign-workspace wait = (%q, payload valid=%v), want (pending, false)", status, payload.Valid)
	}
}

func TestSQLSignalWaitStore_DeliverTopic_UnwatchedTopicIsNoOpNotError(t *testing.T) {
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	ctx := context.Background()

	armWait(t, s, "ws_a", "run_1", "mission.status_change")

	woken, err := s.DeliverTopic(ctx, "ws_a", "nobody.listens", "p")
	if err != nil {
		t.Fatalf("deliver topic on an unwatched topic returned an error: %v — an event nobody waits on is normal, not a failure", err)
	}
	if len(woken) != 0 {
		t.Fatalf("woken = %v, want empty", woken)
	}
	if status, _ := waitStatus(t, db, "run_1"); status != "pending" {
		t.Errorf("run_1 status = %q, want pending", status)
	}
}

// A wait already delivered (or consumed) is not re-delivered: the claim is on
// status='pending', so a second delivery of the same topic wakes nothing.
func TestSQLSignalWaitStore_DeliverTopic_SecondDeliveryClaimsNothing(t *testing.T) {
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	ctx := context.Background()

	armWait(t, s, "ws_a", "run_1", "topic")

	first, err := s.DeliverTopic(ctx, "ws_a", "topic", "one")
	if err != nil {
		t.Fatalf("first deliver: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first deliver woke %v, want 1 run", first)
	}
	second, err := s.DeliverTopic(ctx, "ws_a", "topic", "two")
	if err != nil {
		t.Fatalf("second deliver: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second deliver woke %v, want nothing — a delivered wait is already claimed", second)
	}
	if _, payload := waitStatus(t, db, "run_1"); payload.String != "one" {
		t.Errorf("payload = %q, want %q (the second delivery must not overwrite the claimed one)", payload.String, "one")
	}
}

// Two producers can emit the same internal event at the same instant (two
// issues transitioning, two webhook deliveries). Each parked run must be
// claimed by exactly ONE of them: a run id returned to two callers means two
// ResumeAfterSignal calls on the same parked run.
func TestSQLSignalWaitStore_DeliverTopic_ConcurrentDeliveryDoesNotDoubleResume(t *testing.T) {
	db := openSignalWaitTestDB(t)
	s := NewSQLSignalWaitStore(db)
	ctx := context.Background()

	const runs = 40
	want := map[string]bool{}
	for i := 0; i < runs; i++ {
		id := fmt.Sprintf("run_%02d", i)
		want[id] = true
		armWait(t, s, "ws_a", id, "storm")
	}

	const callers = 32
	var (
		mu    sync.Mutex
		seen  = map[string]int{}
		errs  []error
		wg    sync.WaitGroup
		start = make(chan struct{})
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			woken, err := s.DeliverTopic(ctx, "ws_a", "storm", "p")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			for _, id := range woken {
				seen[id]++
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		t.Errorf("concurrent deliver: %v", err)
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("run %s was returned by %d concurrent callers — each would resume the parked run, double-executing the wait step", id, n)
		}
		if !want[id] {
			t.Errorf("unexpected run id %q in the woken set", id)
		}
	}
	if len(seen) != runs {
		t.Errorf("distinct runs woken = %d, want %d — every parked run must be claimed exactly once across the concurrent callers", len(seen), runs)
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_signal_waits WHERE status = 'pending'`).Scan(&pending); err != nil {
		t.Fatalf("count pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending waits left = %d, want 0", pending)
	}
}

// The topic lookup must be answerable without a run id. Pinned against the
// query planner rather than the result set: (run_id, event_type, status) —
// the only index before this change — cannot serve a workspace+event scan, so
// the delivery would degrade to a full table scan on every internal event.
func TestSQLSignalWaitStore_DeliverTopic_UsesTopicIndex(t *testing.T) {
	db := openSignalWaitTestDB(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN
SELECT id FROM pipeline_signal_waits
WHERE workspace_id = 'ws_a' AND event_type = 'e' AND status = 'pending'`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	if !strings.Contains(plan, "USING INDEX") && !strings.Contains(plan, "USING COVERING INDEX") {
		t.Errorf("topic lookup plan does not use an index:\n%s", plan)
	}
}
