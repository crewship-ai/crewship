package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// fixedClock is the controllable time source §4 requires. Tests advance it
// explicitly; nothing here sleeps, because a sleep-based race test passes for
// the wrong reason on a loaded box.
type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fixedClock {
	return &fixedClock{t: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestStore returns a Store over a real, migrated, file-backed SQLite
// database with the production pragmas. An in-memory fake would not exercise
// the immediate-transaction locking the claim path depends on.
func newTestStore(t *testing.T) (*Store, *sql.DB, *fixedClock) {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	clock := newClock()
	var seq atomic.Uint64
	s := NewStore(db).
		WithClock(clock.Now).
		// Zero jitter keeps retry timing exact; TestBackoff covers the spread.
		WithRand(func(int64) int64 { return 0 }).
		WithIDs(func() string { return fmt.Sprintf("id_%04d", seq.Add(1)) })
	return s, db, clock
}

func accept(t *testing.T, s *Store, db *sql.DB, req AcceptRequest) Receipt {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	r, err := s.AcceptTx(ctx, tx, req)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("accept: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return r
}

func backgroundReq(agent string) AcceptRequest {
	return AcceptRequest{
		WorkspaceID: "ws1", Source: SourceWebhook, Class: ClassBackground, AgentID: agent,
	}
}

func chatReq(agent, session string) AcceptRequest {
	return AcceptRequest{
		WorkspaceID: "ws1", Source: SourceChat, Class: ClassChat, AgentID: agent, SessionID: session,
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// I1: acceptance is atomic with whatever the caller writes alongside it. A
// rollback must leave no work item and no event — the shape of finding W3,
// where a reservation committed before the work it reserved existed.
func TestAcceptTx_RollbackLeavesNoWork(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := s.AcceptTx(ctx, tx, backgroundReq("agent-jamie")); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if got := countRows(t, db, "work_items"); got != 0 {
		t.Errorf("work_items after rollback = %d, want 0", got)
	}
	if got := countRows(t, db, "work_events"); got != 0 {
		t.Errorf("work_events after rollback = %d, want 0", got)
	}
}

func TestAcceptTx_CommitQueuesWorkWithHistory(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	r := accept(t, s, db, backgroundReq("agent-jamie"))
	if r.State != StateQueued {
		t.Errorf("state = %q, want queued", r.State)
	}

	it, err := s.Get(ctx, r.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.Generation != 0 || it.Attempts != 0 {
		t.Errorf("generation/attempts = %d/%d, want 0/0", it.Generation, it.Attempts)
	}
	if it.InputJSON != "{}" {
		t.Errorf("input = %q, want {}", it.InputJSON)
	}

	history, err := s.History(ctx, r.WorkID)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 || history[0].ToState != StateQueued || history[0].Seq != 1 {
		t.Fatalf("history = %+v, want one seq-1 event into queued", history)
	}
}

// I5, and the invariant the River spike showed a queue library does not give
// us: once a newer attempt owns the work, the superseded attempt cannot report
// a result. This is the single most important test in the package.
func TestTransition_StaleAttemptCannotOverwriteNewerOne(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	r := accept(t, s, db, backgroundReq("agent-jamie"))

	first, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-a"})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first.Attempt != 1 || first.Generation != 1 {
		t.Fatalf("first claim attempt/generation = %d/%d, want 1/1", first.Attempt, first.Generation)
	}

	// Worker A stops reporting and never recorded a runtime locator, so recovery
	// may safely return the work to the queue.
	clock.Advance(LeaseDuration + time.Second)
	out, err := s.RecoverExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(out.Requeued) != 1 || out.Requeued[0] != r.WorkID {
		t.Fatalf("requeued = %+v, want [%s]", out.Requeued, r.WorkID)
	}

	second, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-b"})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.Generation != 2 || second.Attempt != 2 {
		t.Fatalf("second claim attempt/generation = %d/%d, want 2/2", second.Attempt, second.Generation)
	}

	// Put attempt 2 into running FIRST, so that `running -> succeeded` is a
	// legal edge and the ONLY thing that can refuse worker A's report is the
	// generation term. Without this the state machine would reject it for an
	// unrelated reason and the test would pass with the fencing removed.
	if err := s.Transition(ctx, TransitionRequest{
		WorkID: r.WorkID, RunID: second.RunID, Generation: second.Generation, To: StateRunning,
	}); err != nil {
		t.Fatalf("live transition to running: %v", err)
	}

	// Worker A now wakes up and reports success for attempt 1.
	err = s.Transition(ctx, TransitionRequest{
		WorkID: r.WorkID, RunID: first.RunID, Generation: first.Generation,
		To: StateSucceeded, Reason: "stale worker reporting late",
	})
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale completion error = %v, want ErrStaleGeneration", err)
	}

	it, err := s.Get(ctx, r.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateRunning {
		t.Errorf("state after stale completion = %q, want running (attempt 2 still owns it)", it.State)
	}

	// The live attempt's report is accepted.
	if err := s.Transition(ctx, TransitionRequest{
		WorkID: r.WorkID, RunID: second.RunID, Generation: second.Generation,
		To: StateSucceeded,
	}); err != nil {
		t.Fatalf("live transition to succeeded: %v", err)
	}
}

// A superseded worker must also stop renewing a lease it no longer holds,
// otherwise it keeps the work alive forever from recovery's point of view.
func TestHeartbeat_RefusedAfterSupersession(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	r := accept(t, s, db, backgroundReq("agent-jamie"))
	first, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-a"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.Heartbeat(ctx, first.RunID, first.Generation); err != nil {
		t.Fatalf("heartbeat while live: %v", err)
	}

	clock.Advance(LeaseDuration + time.Second)
	if _, err := s.RecoverExpiredLeases(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-b"}); err != nil {
		t.Fatalf("second claim: %v", err)
	}

	err = s.Heartbeat(ctx, first.RunID, first.Generation)
	if !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("stale heartbeat error = %v, want ErrStaleGeneration", err)
	}
	_ = r
}

// §4: an expired lease is not permission to start a second process. When the
// attempt got as far as recording a runtime locator, recovery parks the work
// for reconciliation instead of racing a possibly-live runtime.
func TestRecoverExpiredLeases_LocatorMeansReconcileNotRequeue(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	r := accept(t, s, db, backgroundReq("agent-jamie"))
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-a"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	startRuntime(t, s, r.WorkID, c, "crew-1/tmux:agent-jamie-"+c.RunID)

	clock.Advance(LeaseDuration + time.Second)
	out, err := s.RecoverExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(out.Requeued) != 0 {
		t.Errorf("requeued = %+v, want none: a runtime may still be live", out.Requeued)
	}
	if len(out.Reconciliation) != 1 || out.Reconciliation[0] != r.WorkID {
		t.Fatalf("reconciliation = %+v, want [%s]", out.Reconciliation, r.WorkID)
	}

	it, err := s.Get(ctx, r.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateNeedsReconciliation {
		t.Errorf("state = %q, want needs_reconciliation", it.State)
	}

	// And it must not be claimable again while unresolved: that would be the
	// second live runtime T08 exists to catch.
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-b"}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim of unreconciled work = %v, want ErrNoWork", err)
	}
}

// I3 and the release commitment: one Jamie holds one chat and one background
// run at once, and a third piece of work waits rather than starting.
func TestClaim_OneChatPlusOneBackgroundThenQueued(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	chat := accept(t, s, db, chatReq("agent-jamie", "session-1"))
	bg1 := accept(t, s, db, backgroundReq("agent-jamie"))
	bg2 := accept(t, s, db, backgroundReq("agent-jamie"))

	claimedChat, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", Class: ClassChat})
	if err != nil {
		t.Fatalf("claim chat: %v", err)
	}
	if claimedChat.Item.ID != chat.WorkID {
		t.Fatalf("claimed %s, want the chat item %s", claimedChat.Item.ID, chat.WorkID)
	}

	claimedBg, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", Class: ClassBackground})
	if err != nil {
		t.Fatalf("claim background: %v", err)
	}
	if claimedBg.Item.ID != bg1.WorkID {
		t.Fatalf("claimed %s, want the first background item %s", claimedBg.Item.ID, bg1.WorkID)
	}

	// Both are live at the same time — this is the 1+1 shape, at the admission
	// level. Proving it end to end with a real Claude runtime is T06's job.
	for _, id := range []string{chat.WorkID, bg1.WorkID} {
		it, err := s.Get(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if !it.State.Live() {
			t.Fatalf("%s state = %q, want a live state", id, it.State)
		}
	}

	// The third waits. It is queued, not rejected, and not lost.
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("third claim = %v, want ErrNoWork", err)
	}
	it, err := s.Get(ctx, bg2.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateQueued {
		t.Errorf("third item state = %q, want queued", it.State)
	}
}

// An adapter that has not passed T06/T07 stays serial, and narrowing the limits
// is how a caller expresses that.
func TestClaim_SerialAdapterGetsOneRunTotal(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	accept(t, s, db, chatReq("agent-codex", "session-codex"))
	accept(t, s, db, backgroundReq("agent-codex"))

	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", Limits: SerialAgentLimits()}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", Limits: SerialAgentLimits()}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("second claim for a serial adapter = %v, want ErrNoWork", err)
	}
}

// I3: a second message for the same session does not start a competing turn,
// even when the agent and the server both have capacity to spare.
func TestClaim_OneActiveTurnPerSession(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	accept(t, s, db, chatReq("agent-jamie", "session-1"))
	second := accept(t, s, db, chatReq("agent-jamie", "session-1"))

	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("second turn in the same session = %v, want ErrNoWork", err)
	}
	it, err := s.Get(ctx, second.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateQueued {
		t.Errorf("second turn state = %q, want queued", it.State)
	}
}

// §6: background may not borrow the chat reservation. With the background cap
// full, a chat item must still get in.
func TestClaim_BackgroundCannotEatTheChatReservation(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	lim := DefaultLimits()
	// One agent per item, so the per-agent cap never masks the server cap.
	for i := 0; i < lim.ServerBackground; i++ {
		accept(t, s, db, backgroundReq(fmt.Sprintf("agent-bg-%d", i)))
	}
	for i := 0; i < lim.ServerBackground; i++ {
		if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", Class: ClassBackground}); err != nil {
			t.Fatalf("background claim %d: %v", i, err)
		}
	}
	// One more background item cannot start: the background cap is full.
	accept(t, s, db, backgroundReq("agent-bg-extra"))
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", Class: ClassBackground}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("over-cap background claim = %v, want ErrNoWork", err)
	}

	// A person's chat still starts.
	chat := accept(t, s, db, chatReq("agent-jamie", "session-1"))
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", Class: ClassChat})
	if err != nil {
		t.Fatalf("chat claim with background full: %v", err)
	}
	if c.Item.ID != chat.WorkID {
		t.Fatalf("claimed %s, want the chat item", c.Item.ID)
	}
}

// The capacity invariant of §10: concurrent producers must never overbook. Run
// this one under -race; the barrier is a channel, not a sleep.
func TestClaim_ConcurrentDispatchersDoNotOverbook(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	lim := DefaultLimits()
	const items = 40
	for i := 0; i < items; i++ {
		accept(t, s, db, backgroundReq(fmt.Sprintf("agent-%d", i)))
	}

	const dispatchers = 16
	var claimed atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < dispatchers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for {
				_, err := s.Claim(ctx, ClaimOptions{LeaseOwner: fmt.Sprintf("w-%d", i)})
				if errors.Is(err, ErrNoWork) {
					return
				}
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				claimed.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := int(claimed.Load()); got != lim.ServerBackground {
		t.Errorf("claimed %d, want exactly the background cap %d", got, lim.ServerBackground)
	}

	var live int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE state IN ('starting','running')`).Scan(&live); err != nil {
		t.Fatalf("count live: %v", err)
	}
	if live != lim.ServerBackground {
		t.Errorf("live rows = %d, want %d", live, lim.ServerBackground)
	}
	// Every attempt must have a distinct run id and a generation of exactly 1.
	var attempts, distinctRuns int
	if err := db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT run_id) FROM work_attempts`).Scan(&attempts, &distinctRuns); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != live || distinctRuns != attempts {
		t.Errorf("attempts=%d distinct run ids=%d, want both %d", attempts, distinctRuns, live)
	}
}

func TestTransition_RejectsIllegalEdgeAndTerminalWork(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	r := accept(t, s, db, backgroundReq("agent-jamie"))

	// A worker transition with no attempt is refused for NOT BEING BOUND, before
	// the edge is even considered — presenting no attempt is the stale worker's
	// signature, so it is refused first and on its own terms.
	err := s.Transition(ctx, TransitionRequest{WorkID: r.WorkID, To: StateSucceeded})
	if !errors.Is(err, ErrNotBound) {
		t.Fatalf("unbound transition = %v, want ErrNotBound", err)
	}

	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// With a real attempt in hand, the edge itself is what refuses.
	//
	// The example used to be starting -> succeeded. That edge exists now: a
	// short run can finish before the confirmation probe ever polls, and
	// refusing its outcome left the work stuck holding a slot (see
	// TestTransition_ASuccessBeforeConfirmationIsStillRecordable). starting ->
	// waiting is still nonsense — an attempt cannot park at a waitpoint it was
	// never observed to reach — so it carries the assertion instead.
	err = s.Transition(ctx, TransitionRequest{
		WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateWaiting,
	})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("starting->waiting = %v, want ErrIllegalTransition", err)
	}
	mustTransition(t, s, r.WorkID, c, StateRunning)
	mustTransition(t, s, r.WorkID, c, StateSucceeded)

	// A cancel that lost the race to the completion is told the truth, not
	// granted a cancel that did not happen.
	err = s.Transition(ctx, TransitionRequest{
		WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation, To: StateCancelled,
	})
	if !errors.Is(err, ErrTerminal) {
		t.Fatalf("cancel after completion = %v, want ErrTerminal", err)
	}
}

// startRuntime runs the whole start protocol: declare where the runtime will
// be, then confirm it. StartRunning requires the intent, because an attempt
// that reaches `running` without one is an attempt recovery cannot reason
// about — it cannot tell a process that was never created from one we failed
// to write down.
func startRuntime(t *testing.T, s *Store, workID string, c *Claimed, locator string) {
	t.Helper()
	ctx := context.Background()
	if err := s.MarkStarting(ctx, workID, c.RunID, c.Generation, locator); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := s.StartRunning(ctx, workID, c.RunID, c.Generation, locator); err != nil {
		t.Fatalf("start running: %v", err)
	}
}

func mustTransition(t *testing.T, s *Store, workID string, c *Claimed, to State) {
	t.Helper()
	if err := s.Transition(context.Background(), TransitionRequest{
		WorkID: workID, RunID: c.RunID, Generation: c.Generation, To: to,
	}); err != nil {
		t.Fatalf("transition to %s: %v", to, err)
	}
}

// §4: at most 5 attempts in total. The fifth failure is a failure, not a sixth
// retry that never runs.
func TestTransition_RetryExhaustionFailsInsteadOfLooping(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	r := accept(t, s, db, backgroundReq("agent-jamie"))

	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
		if err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if c.Attempt != attempt {
			t.Fatalf("attempt = %d, want %d", c.Attempt, attempt)
		}
		if err := s.Transition(ctx, TransitionRequest{
			WorkID: r.WorkID, RunID: c.RunID, Generation: c.Generation,
			To: StateRetryWait, Reason: "transient provider error",
		}); err != nil {
			t.Fatalf("retry transition on attempt %d: %v", attempt, err)
		}
		// Jitter is pinned to zero in this store, so the item is eligible again
		// immediately; advance anyway so the clock reflects real elapsed time.
		clock.Advance(BackoffCap)
	}

	it, err := s.Get(ctx, r.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateFailed {
		t.Fatalf("state after %d attempts = %q, want failed", MaxAttempts, it.State)
	}
	if it.Attempts != MaxAttempts {
		t.Errorf("attempts = %d, want %d", it.Attempts, MaxAttempts)
	}
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim after exhaustion = %v, want ErrNoWork", err)
	}
}

// §4: work without a deadline never expires on its own; work with one that
// passed while queued expires instead of consuming a slot.
func TestClaim_DeadlineExpiresBeforeStartButOnlyWhenSet(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	withDeadline := backgroundReq("agent-a")
	withDeadline.DeadlineAt = clock.Now().Add(time.Minute)
	deadlined := accept(t, s, db, withDeadline)
	openEnded := accept(t, s, db, backgroundReq("agent-b"))

	clock.Advance(2 * time.Minute)

	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if c.Item.ID != openEnded.WorkID {
		t.Fatalf("claimed %s, want the item without a deadline", c.Item.ID)
	}

	it, err := s.Get(ctx, deadlined.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateExpired {
		t.Errorf("deadlined item = %q, want expired", it.State)
	}
	if it.TerminalAt == nil {
		t.Error("expired item has no terminal_at")
	}
}

func TestBackoff_FullJitterStaysInsideItsWindow(t *testing.T) {
	tests := []struct {
		attempt    int
		wantWindow time.Duration
	}{
		{attempt: 0, wantWindow: BackoffBase}, // clamped to 1
		{attempt: 1, wantWindow: 2 * time.Second},
		{attempt: 2, wantWindow: 4 * time.Second},
		{attempt: 3, wantWindow: 8 * time.Second},
		{attempt: 9, wantWindow: BackoffCap},
		{attempt: 50, wantWindow: BackoffCap},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("attempt_%d", tc.attempt), func(t *testing.T) {
			// The maximum the jitter can return is window-1 nanosecond.
			max := Backoff(tc.attempt, func(n int64) int64 { return n - 1 })
			if max != tc.wantWindow-1 {
				t.Errorf("max backoff = %v, want %v", max, tc.wantWindow-1)
			}
			if min := Backoff(tc.attempt, func(int64) int64 { return 0 }); min != 0 {
				t.Errorf("min backoff = %v, want 0 (full jitter reaches zero)", min)
			}
		})
	}
}

func TestStateMachine_EdgeTable(t *testing.T) {
	tests := []struct {
		from, to State
		want     bool
	}{
		{StateQueued, StateStarting, true},
		{StateQueued, StateRunning, false},
		{StateQueued, StateSucceeded, false},
		{StateStarting, StateRunning, true},
		{StateStarting, StateQueued, true},
		// A runtime can finish before the confirmation probe ever polls.
		{StateStarting, StateSucceeded, true},
		{StateRunning, StateWaiting, true},
		{StateWaiting, StateRunning, true},
		{StateRunning, StateSucceeded, true},
		{StateRetryWait, StateQueued, true},
		{StateSucceeded, StateRunning, false},
		{StateFailed, StateQueued, false},
		{StateCancelled, StateRunning, false},
		{StateExpired, StateQueued, false},
		{StateNeedsReconciliation, StateQueued, true},
		{StateNeedsReconciliation, StateSucceeded, true},
		{StateRunning, StateRunning, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if got := CanTransition(tc.from, tc.to); got != tc.want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}

	// Nothing leaves a terminal state.
	for _, term := range []State{StateSucceeded, StateFailed, StateExpired, StateCancelled} {
		if !term.Terminal() {
			t.Errorf("%s should be terminal", term)
		}
		if len(allowed[term]) != 0 {
			t.Errorf("%s has outgoing edges %v; terminal history is not rewritten", term, allowed[term])
		}
	}
}

// `waiting` releases the execution slot but keeps the session occupied, so a
// later turn cannot overtake a parked one (§4, §7).
func TestWaiting_FreesTheSlotButNotTheSession(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	first := accept(t, s, db, chatReq("agent-jamie", "session-1"))
	second := accept(t, s, db, chatReq("agent-jamie", "session-1"))

	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	mustTransition(t, s, first.WorkID, c, StateRunning)
	mustTransition(t, s, first.WorkID, c, StateWaiting)

	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim while the session is parked = %v, want ErrNoWork", err)
	}
	it, err := s.Get(ctx, second.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateQueued {
		t.Errorf("queued turn = %q, want queued", it.State)
	}

	// Once the parked turn finishes, the next one runs.
	mustTransition(t, s, first.WorkID, c, StateRunning)
	mustTransition(t, s, first.WorkID, c, StateSucceeded)
	next, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim after the parked turn finished: %v", err)
	}
	if next.Item.ID != second.WorkID {
		t.Fatalf("claimed %s, want the queued turn %s", next.Item.ID, second.WorkID)
	}
}

// Recovery returns an abandoned attempt to the queue without consulting the
// attempt count, so an item that loses its lease on its LAST attempt comes back
// eligible and out of budget. It must be failed and stepped over — not left
// queued forever, and above all not allowed to stall the work behind it.
//
// The first version of Claim returned ErrNoWork from inside that branch, which
// rolled the failure back with the transaction AND stopped the scan. One
// exhausted item then blocked the whole queue, permanently, on every poll.
func TestClaim_ExhaustedItemIsFailedAndDoesNotStallTheQueue(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	doomed := accept(t, s, db, backgroundReq("agent-doomed"))
	healthy := accept(t, s, db, backgroundReq("agent-healthy"))

	// Burn every attempt but the last through ordinary retries.
	for attempt := 1; attempt < MaxAttempts; attempt++ {
		c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", AgentID: "agent-doomed"})
		if err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		if err := s.Transition(ctx, TransitionRequest{
			WorkID: doomed.WorkID, RunID: c.RunID, Generation: c.Generation,
			To: StateRetryWait, Reason: "transient",
		}); err != nil {
			t.Fatalf("retry %d: %v", attempt, err)
		}
	}

	// The last attempt is claimed and then abandoned: lease expires with no
	// runtime locator, so recovery puts it back on the queue.
	last, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w", AgentID: "agent-doomed"})
	if err != nil {
		t.Fatalf("final claim: %v", err)
	}
	if last.Attempt != MaxAttempts {
		t.Fatalf("final attempt = %d, want %d", last.Attempt, MaxAttempts)
	}
	clock.Advance(LeaseDuration + time.Second)
	out, err := s.RecoverExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(out.Requeued) != 1 {
		t.Fatalf("requeued = %+v, want the exhausted item back on the queue", out.Requeued)
	}

	// The next claim must skip the doomed item and hand back the healthy one.
	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim after exhaustion: %v — an exhausted item must not stall the queue", err)
	}
	if c.Item.ID != healthy.WorkID {
		t.Fatalf("claimed %s, want the healthy item %s", c.Item.ID, healthy.WorkID)
	}

	// And the failure must be durable, not rolled back with the scan.
	it, err := s.Get(ctx, doomed.WorkID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if it.State != StateFailed {
		t.Errorf("exhausted item = %q, want failed", it.State)
	}
	if it.TerminalAt == nil {
		t.Error("exhausted item has no terminal_at, so the failure did not commit")
	}
}

// The review found this one: recovery parked A for reconciliation, and the
// admission counts only looked at starting/running — so B for the same agent
// and the same session could start while A's runtime may well still have been
// alive under its recorded locator. The comment claimed a stronger guarantee
// than the code delivered.
//
// needs_reconciliation holds its conflicting capacity until somebody resolves
// it. That is the difference between "we lost track of a process" and "there is
// no process".
func TestNeedsReconciliation_HoldsCapacityUntilResolved(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	// A and B are the same agent and the same session — every gate that could
	// stop B is exercised at once.
	a := accept(t, s, db, chatReq("agent-jamie", "session-1"))
	b := accept(t, s, db, chatReq("agent-jamie", "session-1"))

	claimA, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-a"})
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	if claimA.Item.ID != a.WorkID {
		t.Fatalf("claimed %s, want A", claimA.Item.ID)
	}
	// A reaches a real runtime and records where it is. This is what makes its
	// disappearance ambiguous rather than clean.
	locator := "crew-1/tmux:agent-jamie-" + claimA.RunID
	startRuntime(t, s, a.WorkID, claimA, locator)

	clock.Advance(LeaseDuration + time.Second)
	out, err := s.RecoverExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(out.Reconciliation) != 1 || out.Reconciliation[0] != a.WorkID {
		t.Fatalf("reconciliation = %+v, want [%s]", out.Reconciliation, a.WorkID)
	}

	// B must NOT start. Its agent may still be executing A.
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-b"}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim B while A is unreconciled = %v, want ErrNoWork", err)
	}
	itB, err := s.Get(ctx, b.WorkID)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if itB.State != StateQueued {
		t.Errorf("B = %q, want queued", itB.State)
	}

	// Nor may an unrelated agent's work exceed the server total because the
	// reconciling item was counted as free. Fill every remaining slot and check
	// the ledger agrees with the cap.
	lim := DefaultLimits()
	for i := 0; i < lim.ServerTotal+2; i++ {
		accept(t, s, db, backgroundReq(fmt.Sprintf("agent-filler-%d", i)))
	}
	for {
		if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-fill"}); errors.Is(err, ErrNoWork) {
			break
		} else if err != nil {
			t.Fatalf("fill claim: %v", err)
		}
	}
	var holding int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM work_items WHERE state IN ('starting','running','needs_reconciliation')`,
	).Scan(&holding); err != nil {
		t.Fatalf("count: %v", err)
	}
	if holding > lim.ServerTotal {
		t.Errorf("%d items hold execution slots, cap is %d — the reconciling item was counted as free",
			holding, lim.ServerTotal)
	}

	// Resolving it explicitly is what frees the capacity, and only then does B run.
	// Resolution is the ADMIN path, not a worker transition: A's attempt is
	// over, so there is no live attempt to present and the worker path is
	// unsatisfiable here by construction.
	if err := s.Resolve(ctx, a.WorkID, StateFailed, "operator",
		"checked the container; the runtime was gone"); err != nil {
		t.Fatalf("resolve A: %v", err)
	}
	claimB, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker-b", AgentID: "agent-jamie"})
	if err != nil {
		t.Fatalf("claim B after A was resolved: %v", err)
	}
	if claimB.Item.ID != b.WorkID {
		t.Fatalf("claimed %s, want B", claimB.Item.ID)
	}
}

// The other half of the review's first finding: `waiting` must keep the session
// but give the execution slot back, and needs_reconciliation must keep both.
func TestStateSets_HoldExactlyWhatTheyClaim(t *testing.T) {
	tests := []struct {
		state    State
		slot     bool
		session  bool
		terminal bool
	}{
		{StateQueued, false, false, false},
		{StateStarting, true, true, false},
		{StateRunning, true, true, false},
		{StateWaiting, false, true, false},
		{StateRetryWait, false, false, false},
		{StateNeedsReconciliation, true, true, false},
		{StateSucceeded, false, false, true},
		{StateFailed, false, false, true},
		{StateExpired, false, false, true},
		{StateCancelled, false, false, true},
	}
	seen := map[State]bool{}
	for _, tc := range tests {
		seen[tc.state] = true
		t.Run(string(tc.state), func(t *testing.T) {
			if got := tc.state.HoldsExecutionSlot(); got != tc.slot {
				t.Errorf("HoldsExecutionSlot() = %v, want %v", got, tc.slot)
			}
			if got := tc.state.OccupiesSession(); got != tc.session {
				t.Errorf("OccupiesSession() = %v, want %v", got, tc.session)
			}
			if got := tc.state.Terminal(); got != tc.terminal {
				t.Errorf("Terminal() = %v, want %v", got, tc.terminal)
			}
		})
	}
	for _, st := range allStates {
		if !seen[st] {
			t.Errorf("state %q is not covered by this table; a new state must declare what it holds", st)
		}
	}
	// The SQL fragments must agree with the predicates, since four queries read
	// them and a divergence would be invisible until a capacity bug.
	if !strings.Contains(sqlHoldsExecutionSlot, string(StateNeedsReconciliation)) {
		t.Errorf("sqlHoldsExecutionSlot = %q, must include needs_reconciliation", sqlHoldsExecutionSlot)
	}
	if strings.Contains(sqlHoldsExecutionSlot, string(StateWaiting)) {
		t.Errorf("sqlHoldsExecutionSlot = %q, must NOT include waiting — a parked runtime gives its slot back", sqlHoldsExecutionSlot)
	}
	if !strings.Contains(sqlOccupiesSession, string(StateWaiting)) {
		t.Errorf("sqlOccupiesSession = %q, must include waiting", sqlOccupiesSession)
	}
}

// The review's second finding. The scan used to select the oldest 200 rows and
// then filter them in Go, so a queue whose first 200 items were all blocked hid
// the claimable work behind them — and re-polling walked the same dead prefix
// forever. The fix is not a bigger limit; it is applying the per-row rules in
// SQL so the limit selects from rows that are actually claimable.
//
// Constructing this needs care. The fairness ordering added alongside the fix
// would rescue the obvious version of this test on its own: an item whose agent
// has nothing running sorts ahead of one whose agent is busy, so a free agent's
// work floats to the front regardless of the limit. So the blockers here are
// blocked by their SESSION and carry no agent at all, and everything shares one
// workspace — which makes every ordering key equal and leaves acceptance order
// to decide. The claimable item is then genuinely last, behind 200 others.
func TestClaim_FindsWorkBehindAFullBatchOfBlockedItems(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	const blocked = 200
	if blocked <= candidateBatch {
		t.Fatalf("this test is meaningless unless it exceeds the batch size (%d)", candidateBatch)
	}

	sessionItem := func() AcceptRequest {
		return AcceptRequest{
			WorkspaceID: "ws1", Source: SourceChat, Class: ClassChat,
			SessionID: "session-busy",
		}
	}

	// One turn holds the session. It is the only item with an agent, so every
	// candidate below sees the same zero agent-live-count.
	holder := sessionItem()
	holder.AgentID = "agent-holder"
	held := accept(t, s, db, holder)

	// 200 further turns for that same session: all permanently blocked while the
	// held one runs, all agentless, all in ws1.
	for i := 0; i < blocked; i++ {
		accept(t, s, db, sessionItem())
	}

	// Accepted last, in the same workspace, so acceptance order puts it at
	// position 202 and no ordering key lifts it.
	free := backgroundReq("agent-free")
	free.WorkspaceID = "ws1"
	reachable := accept(t, s, db, free)

	first, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim the session holder: %v", err)
	}
	if first.Item.ID != held.WorkID {
		t.Fatalf("claimed %s, want the session holder %s", first.Item.ID, held.WorkID)
	}

	got, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim behind %d blocked items: %v — a blocked prefix must not hide claimable work", blocked, err)
	}
	if got.Item.ID != reachable.WorkID {
		t.Fatalf("claimed %s (agent %q, session %q), want the free item %s",
			got.Item.ID, got.Item.AgentID, got.Item.SessionID, reachable.WorkID)
	}
}

// §6's fairness, now that the scan can express it: a workspace already using
// capacity yields to one using none, even when its work is older.
func TestClaim_RoundRobinsAcrossWorkspaces(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	busy := func(agent string) AcceptRequest {
		r := backgroundReq(agent)
		r.WorkspaceID = "ws-busy"
		return r
	}
	quiet := func(agent string) AcceptRequest {
		r := backgroundReq(agent)
		r.WorkspaceID = "ws-quiet"
		return r
	}

	// ws-busy queues first and therefore wins on every FIFO tiebreak.
	first := accept(t, s, db, busy("agent-b1"))
	accept(t, s, db, busy("agent-b2"))
	later := accept(t, s, db, quiet("agent-q1"))

	got, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if got.Item.ID != first.WorkID {
		t.Fatalf("first claim took %s, want the oldest item %s", got.Item.ID, first.WorkID)
	}

	// ws-busy now has a run in flight. The next slot goes to the workspace with
	// none, even though ws-busy's remaining item was accepted earlier.
	got, err = s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if got.Item.ID != later.WorkID {
		t.Fatalf("second claim took %s from %q; §6 round-robin should have picked %s from ws-quiet",
			got.Item.ID, got.Item.WorkspaceID, later.WorkID)
	}
}

// Aging: a long-waiting item sorts ahead of younger work in the same class, so
// a steady arrival of new work cannot lap it forever.
func TestClaim_AgedWorkOvertakesYoungerWorkOfEqualPriority(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	// The old item belongs to a workspace that is about to look "busier", so
	// only the aging term can put it first.
	old := backgroundReq("agent-old")
	old.WorkspaceID = "ws-old"
	aged := accept(t, s, db, old)

	clock.Advance(AgingThreshold + time.Second)

	young := backgroundReq("agent-young")
	young.WorkspaceID = "ws-young"
	accept(t, s, db, young)

	got, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got.Item.ID != aged.WorkID {
		t.Fatalf("claimed %s, want the aged item %s — work eligible for more than %s must sort ahead of younger work",
			got.Item.ID, aged.WorkID, AgingThreshold)
	}
}

// The SQL filter and the Go re-check must agree. They are two expressions of
// one rule set, and a divergence would show up as work that the scan offers and
// admission silently drops — which reads exactly like an empty queue.
func TestClaim_SQLFilterAndGoAdmissionAgree(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	// A mixture that exercises every per-row rule: a busy session, an agent at
	// its chat cap, an agent at its total cap, and free work.
	accept(t, s, db, chatReq("agent-session", "session-x"))
	accept(t, s, db, chatReq("agent-session", "session-x"))
	accept(t, s, db, chatReq("agent-both", "session-y"))
	accept(t, s, db, backgroundReq("agent-both"))
	accept(t, s, db, backgroundReq("agent-both"))
	for i := 0; i < 5; i++ {
		accept(t, s, db, backgroundReq(fmt.Sprintf("agent-free-%d", i)))
	}

	// Claim until nothing is left, checking at every step that each row the SQL
	// offered was also admitted by the Go rules.
	for {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		live, err := s.countLiveTx(ctx, tx)
		if err != nil {
			t.Fatalf("count live: %v", err)
		}
		classes, ok := live.admissibleClasses(DefaultLimits(), "")
		if !ok {
			_ = tx.Rollback()
			break
		}
		candidates, err := s.scanCandidatesTx(ctx, tx, ClaimOptions{Limits: DefaultLimits()}, classes, s.now().UTC())
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, it := range candidates {
			admitted, err := s.admitsTx(ctx, tx, it, DefaultLimits(), live)
			if err != nil {
				t.Fatalf("admits: %v", err)
			}
			if !admitted {
				t.Fatalf("SQL offered %s (agent %q, class %q, session %q) but the Go rules refused it — "+
					"the two rule sets have drifted apart, and the symptom is work that looks like an empty queue",
					it.ID, it.AgentID, it.Class, it.SessionID)
			}
		}
		_ = tx.Rollback()
		if len(candidates) == 0 {
			break
		}
		if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"}); errors.Is(err, ErrNoWork) {
			break
		} else if err != nil {
			t.Fatalf("claim: %v", err)
		}
	}
}

// A run that finishes before anything observed it running must still be able to
// record that it finished.
//
// The confirmation probe polls, so an attempt that ends quickly — or one whose
// first poll is slow — settles while it is still `starting`. Without the
// starting -> succeeded edge that outcome is unwritable: the work keeps holding
// an execution slot in a live state, its real result exists nowhere, and sixty
// seconds later lease recovery parks it and tells an operator that a run which
// SUCCEEDED needs reconciliation.
//
// Found by the end-to-end pass in internal/api, on the most ordinary case there
// is. The unit layer had only ever settled runs it had first confirmed.
func TestTransition_ASuccessBeforeConfirmationIsStillRecordable(t *testing.T) {
	s, db, _ := newTestStore(t)
	ctx := context.Background()

	rec := accept(t, s, db, backgroundReq("agent-fast"))
	claimed, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "d1", Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.MarkStarting(ctx, rec.WorkID, claimed.RunID, claimed.Generation, "agent-run:"+claimed.RunID); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	// No StartRunning: the probe never got a turn.

	if err := s.Transition(ctx, TransitionRequest{
		WorkID: rec.WorkID, RunID: claimed.RunID, Generation: claimed.Generation,
		To: StateSucceeded, Reason: "completed",
	}); err != nil {
		t.Fatalf("a run that succeeded before confirmation could not record it: %v", err)
	}

	got, err := s.Get(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded {
		t.Errorf("state = %q, want succeeded", got.State)
	}
	// And the history says what actually happened: never observed running.
	events, err := s.History(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.ToState == StateRunning {
			t.Error("a `running` transition was synthesised for a run nothing ever observed running")
		}
	}
}

// The attempt NUMBER and the attempt BUDGET are two different quantities, and
// the ledger cannot store them in one field.
//
// work_attempts is UNIQUE(work_id, attempt), so an attempt number, once used,
// is used. The budget is what a deferral gives back — work held on a person
// must not spend its five attempts waiting for one. While the two were the same
// column, the claim after a deferral tried to reuse a number that already had a
// row, and the insert failed with a constraint error the dispatcher could only
// log and retry: a hot loop that never ran the work and never gave up either.
func TestDefer_GivesTheBudgetBackWithoutReusingAnAttemptNumber(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	rec := accept(t, s, db, backgroundReq("agent-held"))

	var runIDs []string
	for i := 0; i < 3; i++ {
		claimed, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "d", Limits: DefaultLimits()})
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		runIDs = append(runIDs, claimed.RunID)
		if err := s.Defer(ctx, rec.WorkID, claimed.RunID, claimed.Generation,
			clock.Now().Add(-time.Second), "held on an operator"); err != nil {
			t.Fatalf("defer %d: %v", i, err)
		}
		it, err := s.Get(ctx, rec.WorkID)
		if err != nil {
			t.Fatal(err)
		}
		if it.State != StateQueued {
			t.Fatalf("after deferral %d the work is %q, want queued", i, it.State)
		}
		if it.Attempts != 0 {
			t.Fatalf("after deferral %d the budget is %d, want 0 — a held item would run out of "+
				"attempts waiting for a person", i, it.Attempts)
		}
	}

	// Three attempt rows, three distinct numbers, three distinct run ids: the
	// history of having looked is kept, which is the reason the numbers cannot
	// be reused.
	rows, err := db.Query(`SELECT attempt, run_id FROM work_attempts WHERE work_id = ? ORDER BY attempt`, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[int]string{}
	for rows.Next() {
		var n int
		var runID string
		if err := rows.Scan(&n, &runID); err != nil {
			t.Fatal(err)
		}
		if prev, dup := seen[n]; dup {
			t.Fatalf("attempt number %d used by both %s and %s", n, prev, runID)
		}
		seen[n] = runID
	}
	if len(seen) != 3 {
		t.Errorf("%d attempt rows, want 3 — a deferral must leave a record that it happened", len(seen))
	}

	// And the budget really is intact: five full attempts remain.
	for i := 0; i < MaxAttempts; i++ {
		claimed, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "d", Limits: DefaultLimits()})
		if err != nil {
			t.Fatalf("claim %d after the deferrals: %v", i, err)
		}
		if err := s.Transition(ctx, TransitionRequest{
			WorkID: rec.WorkID, RunID: claimed.RunID, Generation: claimed.Generation,
			To: StateRetryWait, Reason: "failed",
		}); err != nil {
			t.Fatalf("fail attempt %d: %v", i, err)
		}
		clock.Advance(BackoffCap + time.Second)
	}
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "d", Limits: DefaultLimits()}); !errors.Is(err, ErrNoWork) {
		t.Errorf("claim after %d real attempts = %v, want ErrNoWork", MaxAttempts, err)
	}
	it, err := s.Get(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != StateFailed {
		t.Errorf("state = %q after exhausting the budget, want failed — deferrals must not "+
			"extend it either", it.State)
	}
	_ = runIDs
}

// A cancel that lands between the claim and a deferral must survive the
// deferral.
//
// The request is recorded on the attempt that is current when it arrives. A
// deferral closes that attempt and returns the work to the queue, and the next
// claim opens a fresh one with nothing on it — so a user who was told
// "requested" watches the work run anyway once the hold clears. Review P1 on
// the deferral work: the store has to decide the cancel INSIDE the deferral's
// transaction, because any check outside it is a second race of the same shape.
func TestDefer_ACancelRequestedDuringTheHoldIsHonoured(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	rec := accept(t, s, db, backgroundReq("agent-held"))
	claimed, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "d", Limits: DefaultLimits()})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The dispatcher checked for a cancel right after the claim and found
	// none. Now, while it is asking the authorizer, someone cancels.
	out, err := s.RequestCancel(ctx, rec.WorkID, "operator", "changed my mind")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if out.Outcome != CancelOutcomeRequested {
		t.Fatalf("cancel outcome = %q, want requested (the attempt is live)", out.Outcome)
	}

	// The authorizer says "not yet" and the dispatcher defers.
	if err := s.Defer(ctx, rec.WorkID, claimed.RunID, claimed.Generation,
		clock.Now().Add(-time.Second), "agent is PENDING_REVIEW"); err != nil {
		t.Fatalf("defer: %v", err)
	}

	it, err := s.Get(ctx, rec.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != StateCancelled {
		t.Fatalf("after a cancel and a deferral the work is %q, want cancelled — the deferral "+
			"closed the attempt the request was recorded on, and the request went with it", it.State)
	}
	// And nothing can pick it up again.
	if _, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "d", Limits: DefaultLimits()}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("cancelled work was claimed again: %v", err)
	}
}
