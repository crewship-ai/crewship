package work

// Tests for the §10 instrumentation this package owns: the process counters
// no table can answer, the acceptance-latency window, the state-set accessors
// internal/server's collectors build their SQL from, and the four journal
// entries §10 sends the ids to.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ── Counters ────────────────────────────────────────────────────────────

func TestRejectReasonFold(t *testing.T) {
	tests := []struct {
		name string
		in   RejectReason
		want RejectReason
	}{
		{"declared signature", RejectSignature, RejectSignature},
		{"declared budget", RejectBudget, RejectBudget},
		{"declared capacity", RejectCapacity, RejectCapacity},
		{"declared payload", RejectPayload, RejectPayload},
		{"declared unauthorized", RejectUnauthorized, RejectUnauthorized},
		{"declared other", RejectOther, RejectOther},
		// The whole point of the closed set: an error string used as a reason
		// must never mint a label value.
		{"free text", RejectReason("connection reset by peer"), RejectOther},
		{"empty", RejectReason(""), RejectOther},
		{"case mismatch", RejectReason("Signature"), RejectOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Fold(); got != tc.want {
				t.Errorf("Fold(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRecordCountersAccumulate(t *testing.T) {
	resetMetricsForTest()
	t.Cleanup(resetMetricsForTest)

	RecordRejected(RejectSignature)
	RecordRejected(RejectSignature)
	RecordRejected(RejectBudget)
	RecordRejected("nobody declared this")
	RecordDuplicate()
	RecordCleanupFailure()
	RecordCleanupFailure()
	RecordCleanupFailure()
	RecordCapacityViolation()

	got := ReadCounters()
	if got.Rejected[RejectSignature] != 2 {
		t.Errorf("signature rejects = %d, want 2", got.Rejected[RejectSignature])
	}
	if got.Rejected[RejectBudget] != 1 {
		t.Errorf("budget rejects = %d, want 1", got.Rejected[RejectBudget])
	}
	if got.Rejected[RejectOther] != 1 {
		t.Errorf("other rejects = %d, want 1 (the undeclared reason folds)", got.Rejected[RejectOther])
	}
	if got.Rejected[RejectPayload] != 0 {
		t.Errorf("payload rejects = %d, want 0", got.Rejected[RejectPayload])
	}
	if got.Duplicates != 1 || got.CleanupFailures != 3 || got.CapacityViolations != 1 {
		t.Errorf("duplicates/cleanup/capacity = %d/%d/%d, want 1/3/1",
			got.Duplicates, got.CleanupFailures, got.CapacityViolations)
	}

	// The snapshot must be a copy: a caller that mutates it (a collector
	// zero-filling its own map, say) must not reach back into the counters.
	got.Rejected[RejectSignature] = 999
	if again := ReadCounters(); again.Rejected[RejectSignature] != 2 {
		t.Errorf("snapshot aliases the live counter: got %d after mutating the copy", again.Rejected[RejectSignature])
	}
}

func TestReadCountersCoversEveryDeclaredReason(t *testing.T) {
	// A reason missing from the snapshot would render as an absent series
	// rather than a zero-filled one, breaking the "stable series set from the
	// first scrape" convention the exposition depends on.
	got := ReadCounters()
	for _, r := range RejectReasons {
		if _, ok := got.Rejected[r]; !ok {
			t.Errorf("snapshot has no entry for declared reason %q", r)
		}
	}
	if len(got.Rejected) != len(RejectReasons) {
		t.Errorf("snapshot has %d reasons, want exactly the %d declared", len(got.Rejected), len(RejectReasons))
	}
}

// ── Acceptance latency ──────────────────────────────────────────────────

func TestAcceptanceLatencyWindow(t *testing.T) {
	resetMetricsForTest()
	t.Cleanup(resetMetricsForTest)

	if got := AcceptanceLatencySamples(); len(got) != 0 {
		t.Fatalf("fresh window has %d samples, want 0 — an empty window must produce NO quantile, not a zero one", len(got))
	}

	RecordAcceptanceLatency(100 * time.Millisecond)
	RecordAcceptanceLatency(300 * time.Millisecond)
	// A clock stepping backwards mid-request must not enter the percentile.
	RecordAcceptanceLatency(-5 * time.Second)

	got := AcceptanceLatencySamples()
	if len(got) != 2 {
		t.Fatalf("samples = %v, want 2 (the negative duration dropped)", got)
	}
	sum := got[0] + got[1]
	if sum < 0.39 || sum > 0.41 {
		t.Errorf("samples %v sum to %v, want ~0.4s", got, sum)
	}
}

func TestAcceptanceLatencyRingWrapsAtCapacity(t *testing.T) {
	resetMetricsForTest()
	t.Cleanup(resetMetricsForTest)

	// Fill past capacity. The window is bounded so a long-running process
	// cannot grow this buffer without limit; the oldest samples fall out.
	for i := 0; i < acceptanceLatencyWindow+50; i++ {
		RecordAcceptanceLatency(time.Duration(i) * time.Millisecond)
	}
	got := AcceptanceLatencySamples()
	if len(got) != acceptanceLatencyWindow {
		t.Fatalf("samples = %d, want the window cap %d", len(got), acceptanceLatencyWindow)
	}
	// The first 50 values (0ms..49ms) were overwritten, so the smallest
	// retained sample is 50ms.
	min := got[0]
	for _, v := range got {
		if v < min {
			min = v
		}
	}
	if min < 0.049 {
		t.Errorf("smallest retained sample = %v, want >= 0.050s: the ring did not drop the oldest 50", min)
	}
}

// ── State sets ──────────────────────────────────────────────────────────

func TestAllStatesMatchesTheTransitionTableDomain(t *testing.T) {
	got := AllStates()
	if len(got) == 0 {
		t.Fatal("AllStates is empty")
	}
	seen := map[State]bool{}
	for _, s := range got {
		if !s.valid() {
			t.Errorf("AllStates includes %q, which valid() rejects", s)
		}
		if seen[s] {
			t.Errorf("AllStates lists %q twice", s)
		}
		seen[s] = true
	}
	// Every state the machine can transition FROM or TO must be in the list,
	// or a caller bucketing by AllStates silently loses rows.
	for from, tos := range allowed {
		if !seen[from] {
			t.Errorf("transition table has source state %q, absent from AllStates", from)
		}
		for _, to := range tos {
			if !seen[to] {
				t.Errorf("transition table has target state %q, absent from AllStates", to)
			}
		}
	}

	// The accessor must hand out a copy: a caller sorting or truncating the
	// result must not reorder the SQL fragments derived from the same slice.
	got[0] = State("clobbered")
	if AllStates()[0] == State("clobbered") {
		t.Error("AllStates returns the package's own slice, not a copy")
	}
}

func TestStatesHoldingExecutionSlotMatchesThePredicate(t *testing.T) {
	got := StatesHoldingExecutionSlot()
	inSet := map[State]bool{}
	for _, s := range got {
		inSet[s] = true
	}
	for _, s := range AllStates() {
		if s.HoldsExecutionSlot() != inSet[s] {
			t.Errorf("state %q: HoldsExecutionSlot()=%v but present in the set=%v",
				s, s.HoldsExecutionSlot(), inSet[s])
		}
	}
	// The one that is easy to get wrong, and the reason the accessor exists:
	// needs_reconciliation MUST count against capacity, or a second runtime
	// can start for work whose first was never confirmed stopped.
	if !inSet[StateNeedsReconciliation] {
		t.Error("needs_reconciliation is not counted as holding a slot")
	}
}

// TestLeaseExpiredReasonPrefixMatchesRecovery pins the interface between this
// package and the /metrics lease-loss counter: the counter selects work_events
// rows by this prefix, so a reworded reason would flatten that series to zero
// with nothing failing. Driving the real recovery is the only way to notice.
func TestLeaseExpiredReasonPrefixMatchesRecovery(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	// Two items so both recovery branches are exercised — the one that
	// requeues and the one that parks for reconciliation.
	accept(t, s, db, AcceptRequest{WorkspaceID: "ws", Source: SourceWebhook, Class: ClassBackground, AgentID: "a1"})
	r2 := accept(t, s, db, AcceptRequest{WorkspaceID: "ws", Source: SourceWebhook, Class: ClassBackground, AgentID: "a2"})

	var withRuntime *Claimed
	for i := 0; i < 2; i++ {
		c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "d1"})
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if c.Item.ID == r2.WorkID {
			withRuntime = c
		}
	}
	if withRuntime == nil {
		t.Fatal("the item under test was never claimed")
	}
	if err := s.MarkStarting(ctx, r2.WorkID, withRuntime.RunID, withRuntime.Generation, "crew/run-1"); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := s.StartRunning(ctx, r2.WorkID, withRuntime.RunID, withRuntime.Generation, "crew/run-1"); err != nil {
		t.Fatalf("start running: %v", err)
	}

	clock.Advance(LeaseDuration * 3)
	out, err := s.RecoverExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(out.Requeued) != 1 || len(out.Reconciliation) != 1 {
		t.Fatalf("recovery = %d requeued / %d reconciliation, want 1 / 1", len(out.Requeued), len(out.Reconciliation))
	}

	var total, prefixed int
	if err := db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN reason LIKE ? || '%' THEN 1 ELSE 0 END), 0)
		  FROM work_events
		 WHERE to_state IN ('queued','needs_reconciliation') AND from_state IN ('starting','running')`,
		LeaseExpiredReasonPrefix).Scan(&total, &prefixed); err != nil {
		t.Fatalf("count recovery events: %v", err)
	}
	if total != 2 {
		t.Fatalf("recovery wrote %d events, want 2", total)
	}
	if prefixed != total {
		t.Errorf("%d of %d recovery events start with %q — the /metrics lease-loss counter selects on that prefix and would undercount",
			prefixed, total, LeaseExpiredReasonPrefix)
	}
}

// ── Journal observer ────────────────────────────────────────────────────

type fakeEmitter struct {
	entries []journal.Entry
	err     error
}

func (f *fakeEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	// Validate is what the real writer runs first, so calling it here means a
	// missing required field fails this test rather than production.
	if err := e.Validate(); err != nil {
		return "", err
	}
	f.entries = append(f.entries, e)
	return "j_test", nil
}

func TestObserverEmitsTheFourLedgerEntries(t *testing.T) {
	f := &fakeEmitter{}
	o := NewObserver(f, nil)
	ctx := context.Background()

	item := &Item{
		ID: "wk_1", WorkspaceID: "ws_1", CrewID: "crew_1", AgentID: "agent_1",
		SessionID: "sess_1", Source: SourceWebhook, SourceRef: "del_1",
		Class: ClassBackground, State: StateQueued, Generation: 3, Attempts: 2,
		Priority: 5, EligibleAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}

	o.Accepted(ctx, item)
	o.Claimed(ctx, &Claimed{Item: item, RunID: "run_1", Attempt: 1, Generation: 1,
		LeaseUntil: time.Date(2026, 9, 10, 12, 1, 0, 0, time.UTC)})
	o.NeedsReconciliation(ctx, item, "run_1", "crew/run-1", "runtime still live")
	o.LeaseLost(ctx, item, "run_1", "", true)

	if len(f.entries) != 4 {
		t.Fatalf("emitted %d entries, want 4", len(f.entries))
	}

	tests := []struct {
		entry        journal.Entry
		wantType     journal.EntryType
		wantSeverity journal.Severity
		wantPayload  map[string]any
	}{
		{f.entries[0], journal.EntryWorkAccepted, journal.SeverityInfo,
			map[string]any{"work_id": "wk_1", "class": "background", "source": "webhook", "source_ref": "del_1"}},
		{f.entries[1], journal.EntryWorkClaimed, journal.SeverityInfo,
			map[string]any{"work_id": "wk_1", "run_id": "run_1", "attempt": 1, "generation": int64(1)}},
		{f.entries[2], journal.EntryWorkNeedsReconciliation, journal.SeverityWarn,
			map[string]any{"work_id": "wk_1", "run_id": "run_1", "runtime_locator": "crew/run-1", "reason": "runtime still live"}},
		{f.entries[3], journal.EntryWorkLeaseLost, journal.SeverityNotice,
			map[string]any{"work_id": "wk_1", "run_id": "run_1", "requeued": true}},
	}
	for _, tc := range tests {
		t.Run(string(tc.wantType), func(t *testing.T) {
			e := tc.entry
			if e.Type != tc.wantType {
				t.Fatalf("type = %q, want %q", e.Type, tc.wantType)
			}
			if e.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", e.Severity, tc.wantSeverity)
			}
			if e.WorkspaceID != "ws_1" || e.AgentID != "agent_1" || e.CrewID != "crew_1" {
				t.Errorf("scope = ws %q / crew %q / agent %q, want ws_1 / crew_1 / agent_1",
					e.WorkspaceID, e.CrewID, e.AgentID)
			}
			for k, want := range tc.wantPayload {
				if got := e.Payload[k]; got != want {
					t.Errorf("payload[%q] = %#v, want %#v", k, got, want)
				}
			}
			// §10 sends the ids here, so they must actually be here.
			if e.Refs["work_id"] != "wk_1" {
				t.Errorf("refs work_id = %#v, want wk_1", e.Refs["work_id"])
			}
			// Nothing may carry the immutable input or a raw body: those stay
			// in the ledger under their own retention.
			for _, forbidden := range []string{"input_json", "raw_body", "body", "payload", "secret", "token"} {
				if _, ok := e.Payload[forbidden]; ok {
					t.Errorf("payload carries %q — the journal keeps identifiers, not content", forbidden)
				}
			}
		})
	}

	// run_id doubles as the trace id so these entries join the run's existing
	// trace instead of starting a second one beside it.
	for _, e := range f.entries[1:] {
		if e.TraceID != "run_1" {
			t.Errorf("%s trace_id = %q, want the run id", e.Type, e.TraceID)
		}
	}
}

func TestObserverLeaseLostSeverityDependsOnOutcome(t *testing.T) {
	f := &fakeEmitter{}
	o := NewObserver(f, nil)
	item := &Item{ID: "wk_1", WorkspaceID: "ws_1"}

	o.LeaseLost(context.Background(), item, "run_1", "crew/run-1", false)
	if len(f.entries) != 1 {
		t.Fatalf("emitted %d entries, want 1", len(f.entries))
	}
	// A lease lost that could NOT be requeued left the work holding capacity,
	// so it is not the same event as one that healed itself.
	if got := f.entries[0].Severity; got != journal.SeverityWarn {
		t.Errorf("severity = %q, want warn when the lease loss was not requeued", got)
	}
	if got := f.entries[0].Payload["requeued"]; got != false {
		t.Errorf("payload requeued = %#v, want false", got)
	}
}

func TestObserverIsSafeWithoutAJournal(t *testing.T) {
	// A nil Emitter yields a nil Observer, and a nil Observer must be usable —
	// otherwise every call site needs a branch, and one of them will forget.
	o := NewObserver(nil, nil)
	if o != nil {
		t.Fatalf("NewObserver(nil) = %#v, want nil", o)
	}
	item := &Item{ID: "wk_1", WorkspaceID: "ws_1"}
	o.Accepted(context.Background(), item)
	o.Claimed(context.Background(), &Claimed{Item: item, RunID: "run_1"})
	o.NeedsReconciliation(context.Background(), item, "run_1", "", "")
	o.LeaseLost(context.Background(), item, "run_1", "", true)

	// Nil arguments are no-ops too: a caller recovering from an error may not
	// have an item to describe, and instrumentation must never be the thing
	// that panics.
	real := NewObserver(&fakeEmitter{}, nil)
	real.Accepted(context.Background(), nil)
	real.Claimed(context.Background(), nil)
	real.Claimed(context.Background(), &Claimed{})
	real.NeedsReconciliation(context.Background(), nil, "", "", "")
	real.LeaseLost(context.Background(), nil, "", "", false)
}

func TestObserverSwallowsEmitFailures(t *testing.T) {
	// A journal write must never fail the dispatch decision it describes: the
	// authoritative record is the work_events row, committed with the state
	// change. The failure is logged, not propagated — there is no error to
	// propagate to.
	f := &fakeEmitter{err: errors.New("journal is down")}
	o := NewObserver(f, nil)
	o.Accepted(context.Background(), &Item{ID: "wk_1", WorkspaceID: "ws_1"})
}

func TestItoa(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {5, "5"}, {12345, "12345"}, {-7, "-7"}} {
		if got := itoa(tc.in); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
