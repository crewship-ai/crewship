package work

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

// retDelivery is one accepted webhook carrying a payload. The body bytes and the
// recorded size agree, because the capacity report reads body_bytes and the
// sweeper reads raw_body, and a fixture where those two disagree would hide a
// bug in either.
func retDelivery(sourceID, endpoint string, body []byte) Delivery {
	return Delivery{
		WorkspaceID:      "ws1",
		EndpointID:       endpoint,
		EndpointKind:     "agent",
		Profile:          "standard-webhooks",
		SourceDeliveryID: sourceID,
		BodySHA256:       "sha256:" + sourceID,
		BodyBytes:        len(body),
		RawBody:          body,
		FilterDecision:   FilterAccepted,
		EventType:        "issues",
	}
}

func retWork() AcceptRequest {
	return AcceptRequest{
		WorkspaceID: "ws1", Source: SourceWebhook, Class: ClassBackground, AgentID: "agent-jamie",
	}
}

func acceptDelivery(t *testing.T, s *Store, db *sql.DB, d Delivery, w AcceptRequest) Receipt {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	r, err := s.AcceptDeliveryTx(ctx, tx, d, w)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("accept delivery %s: %v", d.SourceDeliveryID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return r
}

// finish drives a queued work item to a terminal state along legal edges only,
// so terminal_at is written by the same code the server uses.
//
// It claims first, because a worker transition now has to present a real
// attempt — work, run and generation, bound to one another. The old version
// transitioned with no attempt at all, which the store used to allow and which
// was the shape of the cross-work bug the review reproduced. Going through a
// claim is also simply what happens in production: nothing reaches `running`
// without having been claimed.
func finish(t *testing.T, s *Store, workID string, to State) {
	t.Helper()
	ctx := context.Background()

	// Cancel is not a worker transition; it is its own operation.
	if to == StateCancelled {
		if _, err := s.RequestCancel(ctx, workID, "test", "test"); err != nil {
			t.Fatalf("finish %s: cancel: %v", workID, err)
		}
		return
	}

	c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "finish-helper", WorkID: workID})
	if err != nil {
		t.Fatalf("finish %s: claim: %v", workID, err)
	}
	step := func(from, target State) {
		t.Helper()
		if err := s.Transition(ctx, TransitionRequest{
			WorkID: workID, RunID: c.RunID, Generation: c.Generation, To: target, Reason: "test",
		}); err != nil {
			t.Fatalf("finish %s: %s -> %s: %v", workID, from, target, err)
		}
	}
	if !CanTransition(StateStarting, to) {
		step(StateStarting, StateRunning)
		step(StateRunning, to)
		return
	}
	step(StateStarting, to)
}

func rawBodyOf(t *testing.T, db *sql.DB, deliveryID string) []byte {
	t.Helper()
	var body []byte
	err := db.QueryRow(`SELECT raw_body FROM webhook_deliveries WHERE id = ?`, deliveryID).Scan(&body)
	if err != nil {
		t.Fatalf("read raw body %s: %v", deliveryID, err)
	}
	return body
}

func deliveryExists(t *testing.T, db *sql.DB, deliveryID string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries WHERE id = ?`, deliveryID).Scan(&n); err != nil {
		t.Fatalf("count delivery %s: %v", deliveryID, err)
	}
	return n == 1
}

func sweep(t *testing.T, s *Store, p RetentionPolicy) SweepResult {
	t.Helper()
	res, err := s.Sweep(context.Background(), p)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return res
}

// §6: a non-terminal work item's payload must NEVER be removed. Not when its
// nominal expiry passes, not a year later, not because the row looks stale.
//
// This is the guard the whole retention design exists to protect, so the test
// pushes the clock a long way past every window at once: the raw body's 7 days,
// the dedup 30 days, and the delivery's own explicitly stored expiry, which is
// set here to a moment that has already passed. The work is still queued, so
// none of that may matter.
func TestSweep_NonTerminalPayloadIsNeverRemoved(t *testing.T) {
	s, db, clock := newTestStore(t)

	d := retDelivery("dlv-live", "ep-1", []byte(`{"action":"opened"}`))
	// An expiry that is already in the past when the sweep runs. Even asked for
	// explicitly, it must not take a payload whose work has not finished.
	d.RawBodyExpiresAt = clock.Now().Add(time.Hour)
	r := acceptDelivery(t, s, db, d, retWork())

	clock.Advance(400 * 24 * time.Hour)

	res := sweep(t, s, DefaultRetentionPolicy())

	if res.RawBodiesExpired != 0 {
		t.Errorf("RawBodiesExpired = %d, want 0: the work is still queued", res.RawBodiesExpired)
	}
	if res.DeliveriesRemoved != 0 {
		t.Errorf("DeliveriesRemoved = %d, want 0: a non-terminal work item keeps its dedup key", res.DeliveriesRemoved)
	}
	if got := rawBodyOf(t, db, r.DeliveryID); string(got) != `{"action":"opened"}` {
		t.Errorf("raw body = %q, want the payload intact 400 days into a non-terminal life", got)
	}
	if !deliveryExists(t, db, r.DeliveryID) {
		t.Error("the delivery row was deleted while its work was still queued")
	}

	st, err := s.PayloadStatus(context.Background(), r.DeliveryID)
	if err != nil {
		t.Fatalf("payload status: %v", err)
	}
	if st.Status != PayloadAvailable {
		t.Errorf("PayloadStatus = %q, want %q: replay must still be offered", st.Status, PayloadAvailable)
	}

	// And the work itself is untouched: retention never advances a state.
	it, err := s.Get(context.Background(), r.WorkID)
	if err != nil {
		t.Fatalf("get work: %v", err)
	}
	if it.State != StateQueued {
		t.Errorf("work state = %q, want queued: retention must not move work", it.State)
	}
}

// Once terminal, the payload goes after its window — and the row still says a
// replay is unavailable, rather than looking like a delivery that never carried
// one. That distinction is the whole reason the sweep stamps a tombstone
// instead of writing a bare NULL: §6 requires the UI to explain the missing
// replay, and it can only explain what the row records.
func TestSweep_TerminalPayloadExpiresAndTheRowSaysWhy(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	body := []byte(`{"number":42}`)
	withBody := acceptDelivery(t, s, db, retDelivery("dlv-done", "ep-1", body), retWork())

	// A second delivery that never stored a payload at all — the row this must
	// stay distinguishable from.
	noBody := retDelivery("dlv-nobody", "ep-1", nil)
	noBody.RawBody = nil
	noBody.BodyBytes = 0
	never := acceptDelivery(t, s, db, noBody, retWork())

	terminalAt := clock.Now()
	finish(t, s, withBody.WorkID, StateSucceeded)
	finish(t, s, never.WorkID, StateSucceeded)

	// One second short of the window: still there. A retention that rounds the
	// wrong way loses a payload someone is entitled to replay.
	clock.Advance(DefaultRawBodyRetention - time.Second)
	if res := sweep(t, s, DefaultRetentionPolicy()); res.RawBodiesExpired != 0 {
		t.Fatalf("RawBodiesExpired = %d one second before the window, want 0", res.RawBodiesExpired)
	}
	if got := rawBodyOf(t, db, withBody.DeliveryID); string(got) != string(body) {
		t.Fatalf("raw body = %q one second before the window, want it kept", got)
	}

	clock.Advance(2 * time.Second)
	res := sweep(t, s, DefaultRetentionPolicy())
	if res.RawBodiesExpired != 1 {
		t.Errorf("RawBodiesExpired = %d, want 1", res.RawBodiesExpired)
	}
	if res.RawBodyBytesFreed != int64(len(body)) {
		t.Errorf("RawBodyBytesFreed = %d, want %d", res.RawBodyBytesFreed, len(body))
	}
	if got := rawBodyOf(t, db, withBody.DeliveryID); got != nil {
		t.Errorf("raw body = %q past the window, want it dropped", got)
	}

	// The dedup key and the receipt outlive the payload: 30 days, not 7.
	if !deliveryExists(t, db, withBody.DeliveryID) {
		t.Fatal("the delivery row went with its payload; the receipt has a 30 day window of its own")
	}

	expired, err := s.PayloadStatus(ctx, withBody.DeliveryID)
	if err != nil {
		t.Fatalf("payload status: %v", err)
	}
	if expired.Status != PayloadExpired {
		t.Errorf("PayloadStatus = %q, want %q", expired.Status, PayloadExpired)
	}
	if expired.ReplayAvailable() {
		t.Error("ReplayAvailable() is true for an expired payload; the UI would offer a button that cannot work")
	}
	if want := terminalAt.Add(DefaultRawBodyRetention); !expired.ExpiredAt.Equal(want) {
		t.Errorf("ExpiredAt = %s, want the enforced deadline %s", expired.ExpiredAt, want)
	}
	if expired.BodyBytes != int64(len(body)) {
		t.Errorf("BodyBytes = %d, want %d kept so the UI can say what is gone", expired.BodyBytes, len(body))
	}

	// The delivery that never had one reads differently. If these two collapsed
	// to the same answer the UI could not tell "we deleted it" from "there was
	// nothing", which is the silent-NULL failure this design rejects.
	none, err := s.PayloadStatus(ctx, never.DeliveryID)
	if err != nil {
		t.Fatalf("payload status: %v", err)
	}
	if none.Status != PayloadNone {
		t.Errorf("PayloadStatus for a delivery that stored nothing = %q, want %q", none.Status, PayloadNone)
	}
	if none.Status == expired.Status {
		t.Error("an expired payload and an absent one are indistinguishable")
	}
}

// §6: dedup keys and receipts live at least 30 days from acceptance AND for the
// whole non-terminal life of the work — whichever is longer. Two deliveries,
// same age, one finished and one not.
func TestSweep_DedupRowOutlivesThirtyDaysWhileWorkIsNonTerminal(t *testing.T) {
	s, db, clock := newTestStore(t)

	live := acceptDelivery(t, s, db, retDelivery("dlv-waiting", "ep-1", []byte(`{"a":1}`)), retWork())
	done := acceptDelivery(t, s, db, retDelivery("dlv-finished", "ep-1", []byte(`{"b":2}`)), retWork())
	finish(t, s, done.WorkID, StateFailed)

	clock.Advance(DefaultDedupWindow + 24*time.Hour)
	res := sweep(t, s, DefaultRetentionPolicy())

	if res.DeliveriesRemoved != 1 {
		t.Errorf("DeliveriesRemoved = %d, want 1 (only the finished one)", res.DeliveriesRemoved)
	}
	if deliveryExists(t, db, done.DeliveryID) {
		t.Error("the finished delivery survived its 30 day window")
	}
	if !deliveryExists(t, db, live.DeliveryID) {
		t.Fatal("a dedup key was removed while its work was still queued; a re-delivery would now fork the work")
	}

	// The dedup key still does its job: the same delivery arriving again finds
	// the original work rather than creating a second copy of it.
	again := acceptDelivery(t, s, db, retDelivery("dlv-waiting", "ep-1", []byte(`{"a":1}`)), retWork())
	if !again.Duplicate || again.WorkID != live.WorkID {
		t.Errorf("re-delivery = %+v, want the original work %q with Duplicate set", again, live.WorkID)
	}

	// Once it finishes, the window applies to it too.
	finish(t, s, live.WorkID, StateSucceeded)
	res = sweep(t, s, DefaultRetentionPolicy())
	if res.DeliveriesRemoved != 1 {
		t.Errorf("DeliveriesRemoved after it finished = %d, want 1", res.DeliveriesRemoved)
	}
	if deliveryExists(t, db, live.DeliveryID) {
		t.Error("the delivery survived both windows after its work finished")
	}
}

// A delivery's own raw_body_expires_at can push the deadline out, never pull it
// in. §6 states the 7 days as a DEFAULT retention and the terminal state as the
// minimum, so a row asking for a shorter life does not get to undercut either.
func TestSweep_ExplicitExpiryOnlyExtendsTheWindow(t *testing.T) {
	s, db, clock := newTestStore(t)

	short := retDelivery("dlv-short", "ep-1", []byte(`{"s":1}`))
	short.RawBodyExpiresAt = clock.Now().Add(time.Minute)
	shortR := acceptDelivery(t, s, db, short, retWork())

	long := retDelivery("dlv-long", "ep-1", []byte(`{"l":1}`))
	long.RawBodyExpiresAt = clock.Now().Add(90 * 24 * time.Hour)
	longR := acceptDelivery(t, s, db, long, retWork())

	finish(t, s, shortR.WorkID, StateSucceeded)
	finish(t, s, longR.WorkID, StateSucceeded)

	// An hour in, the short one's own expiry has passed but the policy floor has
	// not. Nothing goes.
	clock.Advance(time.Hour)
	if res := sweep(t, s, DefaultRetentionPolicy()); res.RawBodiesExpired != 0 {
		t.Errorf("RawBodiesExpired = %d an hour after terminal, want 0: the 7 day floor governs",
			res.RawBodiesExpired)
	}

	clock.Advance(DefaultRawBodyRetention)
	res := sweep(t, s, DefaultRetentionPolicy())
	if res.RawBodiesExpired != 1 {
		t.Errorf("RawBodiesExpired = %d, want 1: the floor caught the short one only", res.RawBodiesExpired)
	}
	if rawBodyOf(t, db, shortR.DeliveryID) != nil {
		t.Error("the short-expiry payload survived the 7 day floor")
	}
	if rawBodyOf(t, db, longR.DeliveryID) == nil {
		t.Error("a payload asked to live 90 days was taken at 7")
	}

	clock.Advance(90 * 24 * time.Hour)
	// A dedup window long enough that the delivery row itself survives, so the
	// assertion is about the payload rather than about the row being deleted for
	// an unrelated reason.
	keepRow := DefaultRetentionPolicy()
	keepRow.DedupWindow = 365 * 24 * time.Hour
	sweep(t, s, keepRow)
	if !deliveryExists(t, db, longR.DeliveryID) {
		t.Fatal("the delivery row was removed under a 365 day dedup window")
	}
	if rawBodyOf(t, db, longR.DeliveryID) != nil {
		t.Error("the 90 day payload survived its own expiry")
	}
}

// Retention must report what it did, not merely that it ran. An operator
// watching the database grow needs to see the zero.
func TestSweep_ReportsCountsAndBatches(t *testing.T) {
	s, db, clock := newTestStore(t)

	const n = 7
	for i := 0; i < n; i++ {
		r := acceptDelivery(t, s, db,
			retDelivery(fmt.Sprintf("dlv-%02d", i), "ep-1", []byte(`{"x":1234567890}`)), retWork())
		if i%2 == 0 {
			finish(t, s, r.WorkID, StateSucceeded)
		}
	}
	p := DefaultRetentionPolicy()
	p.Batch = 2 // force several transactions, so the batching is exercised

	// Before anything is due: the four finished payloads are examined and kept,
	// which is the count that separates "nothing was due" from "the predicate
	// never matched".
	if early := sweep(t, s, p); early.RawBodiesExpired != 0 || early.RawBodiesRetained != 4 {
		t.Errorf("sweep before the window = %+v, want 0 expired and 4 retained", early)
	}

	clock.Advance(DefaultRawBodyRetention + time.Hour)
	res := sweep(t, s, p)

	if res.RawBodiesExpired != 4 {
		t.Errorf("RawBodiesExpired = %d, want 4 (the finished half)", res.RawBodiesExpired)
	}
	if res.RawBodyBytesFreed != 4*int64(len(`{"x":1234567890}`)) {
		t.Errorf("RawBodyBytesFreed = %d, want %d", res.RawBodyBytesFreed, 4*len(`{"x":1234567890}`))
	}
	if res.Batches < 2 {
		t.Errorf("Batches = %d, want more than one with Batch=2", res.Batches)
	}
	if res.DeliveriesRemoved != 0 {
		t.Errorf("DeliveriesRemoved = %d, want 0: the 30 day dedup window has not passed", res.DeliveriesRemoved)
	}

	// A second sweep with nothing due reports zeros rather than repeating work.
	again := sweep(t, s, p)
	if again.RawBodiesExpired != 0 || again.DeliveriesRemoved != 0 {
		t.Errorf("second sweep = %+v, want zeros", again)
	}
	if again.RawBodiesRetained != 0 {
		t.Errorf("RawBodiesRetained = %d on a second pass, want 0: the swept rows are no longer candidates",
			again.RawBodiesRetained)
	}
}

// The sweeper runs beside acceptance in production, so it must be correct while
// acceptance is happening — not merely correct when it has the database to
// itself.
//
// The barrier is a closed channel, not a sleep: every goroutine blocks on it and
// they are released together, so the overlap is guaranteed rather than hoped
// for on a loaded box. Run this one under -race.
//
// What is being proved: the deliveries accepted DURING the sweep keep their
// payloads (their work is queued), the terminal ones seeded beforehand lose
// theirs, and nothing errors on either side.
func TestSweep_ConcurrentWithAcceptance(t *testing.T) {
	s, db, clock := newTestStore(t)

	const seeded = 12
	var stale []Receipt
	for i := 0; i < seeded; i++ {
		r := acceptDelivery(t, s, db,
			retDelivery(fmt.Sprintf("old-%02d", i), "ep-1", []byte(`{"old":true}`)), retWork())
		finish(t, s, r.WorkID, StateSucceeded)
		stale = append(stale, r)
	}
	clock.Advance(DefaultRawBodyRetention + time.Hour)

	const accepting = 12
	barrier := make(chan struct{})
	var wg sync.WaitGroup

	fresh := make([]Receipt, accepting)
	acceptErr := make([]error, accepting)
	for i := 0; i < accepting; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			ctx := context.Background()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				acceptErr[i] = err
				return
			}
			r, err := s.AcceptDeliveryTx(ctx, tx,
				retDelivery(fmt.Sprintf("new-%02d", i), "ep-1", []byte(`{"new":true}`)), retWork())
			if err != nil {
				_ = tx.Rollback()
				acceptErr[i] = err
				return
			}
			if err := tx.Commit(); err != nil {
				acceptErr[i] = err
				return
			}
			fresh[i] = r
		}(i)
	}

	const sweepers = 2
	sweepRes := make([]SweepResult, sweepers)
	sweepErr := make([]error, sweepers)
	for i := 0; i < sweepers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-barrier
			p := DefaultRetentionPolicy()
			p.Batch = 3
			sweepRes[i], sweepErr[i] = s.Sweep(context.Background(), p)
		}(i)
	}

	close(barrier)
	wg.Wait()

	for i, err := range acceptErr {
		if err != nil {
			t.Fatalf("acceptance %d during sweep: %v", i, err)
		}
	}
	total := 0
	for i, err := range sweepErr {
		if err != nil {
			t.Fatalf("sweep %d during acceptance: %v", i, err)
		}
		total += sweepRes[i].RawBodiesExpired
	}

	// Two sweepers racing must not double-count: the guard in the UPDATE means
	// the loser's statement affects no rows.
	if total != seeded {
		t.Errorf("payloads expired across both sweepers = %d, want exactly %d", total, seeded)
	}
	for _, r := range stale {
		if rawBodyOf(t, db, r.DeliveryID) != nil {
			t.Errorf("terminal payload %s survived a concurrent sweep", r.DeliveryID)
		}
	}
	for i, r := range fresh {
		if r.DeliveryID == "" {
			t.Fatalf("acceptance %d produced no delivery", i)
		}
		if rawBodyOf(t, db, r.DeliveryID) == nil {
			t.Errorf("payload %s accepted during the sweep was taken while its work was queued", r.DeliveryID)
		}
	}
}

// A payload whose work goes back to non-terminal between the sweeper's walk and
// its write must survive. The walk runs outside a transaction on purpose, so
// its answer can be stale by the time the UPDATE lands; the predicate inside the
// statement is what actually enforces the rule.
//
// needs_reconciliation -> queued is a real edge, so this is a state the ledger
// genuinely reaches, not a contrived one.
func TestSweep_GuardIsInTheStatementNotTheScan(t *testing.T) {
	s, db, clock := newTestStore(t)
	ctx := context.Background()

	r := acceptDelivery(t, s, db, retDelivery("dlv-resurrect", "ep-1", []byte(`{"r":1}`)), retWork())
	finish(t, s, r.WorkID, StateSucceeded)
	clock.Advance(DefaultRawBodyRetention + time.Hour)

	// Take the walk's answer while the work is terminal, then move the work
	// before the batch is written — exactly the interleaving the guard exists
	// for. The state is rewritten directly because the state machine has no edge
	// out of succeeded, and the point here is the SQL guard, not the edge table.
	cands, err := s.walkRawBodyCandidates(ctx, 10, "")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("walk found %d candidates, want 1", len(cands))
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE work_items SET state = 'queued', terminal_at = NULL WHERE id = ?`, r.WorkID); err != nil {
		t.Fatalf("resurrect: %v", err)
	}

	var res SweepResult
	deadlines := []time.Time{cands[0].terminalRef.Add(DefaultRawBodyRetention)}
	if err := s.expireRawBodyBatch(ctx, cands, deadlines,
		clock.Now().Add(-DefaultRawBodyRetention), clock.Now(), &res); err != nil {
		t.Fatalf("expire batch: %v", err)
	}

	if res.RawBodiesExpired != 0 {
		t.Errorf("RawBodiesExpired = %d, want 0: the work was no longer terminal", res.RawBodiesExpired)
	}
	if rawBodyOf(t, db, r.DeliveryID) == nil {
		t.Fatal("a stale scan result removed the payload of work that had gone back to queued")
	}
	if res.RawBodiesRetained != 1 {
		t.Errorf("RawBodiesRetained = %d, want 1: a refused row must be reported, not silently skipped",
			res.RawBodiesRetained)
	}
}
