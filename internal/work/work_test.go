package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if err := s.StartRunning(ctx, r.WorkID, c.RunID, c.Generation, "crew-1/tmux:agent-jamie-"+c.RunID); err != nil {
		t.Fatalf("start running: %v", err)
	}

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

	// queued -> succeeded is not an edge: work cannot finish without running.
	err := s.Transition(ctx, TransitionRequest{WorkID: r.WorkID, To: StateSucceeded})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("queued->succeeded = %v, want ErrIllegalTransition", err)
	}

	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "w"})
	if err != nil {
		t.Fatalf("claim: %v", err)
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
