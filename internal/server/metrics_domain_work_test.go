package server

// Tests for the §10 durable-work-ledger collectors (metrics_domain_work.go).
//
// Every fixture below drives the REAL write path — work.Store.AcceptTx,
// Claim, StartRunning, Transition, RecoverExpiredLeases against a real
// migrated SQLite database — rather than hand-writing rows. That is not
// ceremony: three of these series are defined in terms of strings the work
// package writes (the acceptance event's reason, the lease-expiry reason
// prefix, the state names), and a test that inserts those strings itself
// would keep passing after the producer stopped writing them.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

const workMetricsHostname = "test-host"

// workFixture returns a bare *Server over a migrated database. These
// collectors touch only s.db and s.logger, the same shape b12Fixture uses.
func workFixture(t *testing.T) *Server {
	t.Helper()
	return &Server{db: openTestDB(t), logger: logging.New("error", "json", nil)}
}

// renderWorkMetrics runs the §10 fan-out and returns its text. Deliberately
// NOT the whole /metrics body: this file owns one block, and asserting over
// another collector's output would make an unrelated change red here.
func renderWorkMetrics(t *testing.T, s *Server) string {
	t.Helper()
	var b strings.Builder
	s.collectWorkLedgerMetrics(context.Background(), &b, workMetricsHostname)
	return b.String()
}

// workBaseTime is a fixed instant so every seeded interval is exact and no
// assertion depends on how long the test took to run.
var workBaseTime = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// workStoreAt returns a store whose clock is pinned to at, and whose ids come
// from the production generator — the metrics must be exercised against real
// CUID-shaped ids, because the label-hygiene test's whole job is to prove they
// never reach a label.
func workStoreAt(db *sql.DB, at time.Time) *work.Store {
	return work.NewStore(db).WithClock(func() time.Time { return at })
}

// acceptWork accepts one work item through the real acceptance path and
// returns its id.
func acceptWork(t *testing.T, db *sql.DB, at time.Time, req work.AcceptRequest) string {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin accept: %v", err)
	}
	receipt, err := workStoreAt(db, at).AcceptTx(ctx, tx, req)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("accept: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit accept: %v", err)
	}
	return receipt.WorkID
}

// metricValue returns the value of the single sample of family whose labels
// contain every wanted pair. It fails the test when the family is absent or
// when the match is not unique, so a typo in a metric name can never read as
// "the value happened to be 0".
func metricValue(t *testing.T, exposition, family string, wantLabels map[string]string) float64 {
	t.Helper()
	var found []float64
	for _, line := range strings.Split(exposition, "\n") {
		if !strings.HasPrefix(line, family+"{") {
			continue
		}
		labels, value, ok := parsePromLine(line)
		if !ok {
			t.Fatalf("unparsable exposition line %q", line)
		}
		match := true
		for k, v := range wantLabels {
			if labels[k] != v {
				match = false
				break
			}
		}
		if match {
			found = append(found, value)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("no sample of %s with labels %v in:\n%s", family, wantLabels, exposition)
	default:
		t.Fatalf("%d samples of %s match labels %v, want exactly 1", len(found), family, wantLabels)
	}
	return 0
}

var promLineRe = regexp.MustCompile(`^([a-zA-Z_][a-zA-Z0-9_]*)\{([^}]*)\} (.+)$`)
var promLabelRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)="([^"]*)"`)

func parsePromLine(line string) (labels map[string]string, value float64, ok bool) {
	m := promLineRe.FindStringSubmatch(line)
	if m == nil {
		return nil, 0, false
	}
	labels = map[string]string{}
	for _, pair := range promLabelRe.FindAllStringSubmatch(m[2], -1) {
		labels[pair[1]] = pair[2]
	}
	v, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return nil, 0, false
	}
	return labels, v, true
}

// TestWorkLedgerBucketsCoverEveryState pins the property that makes
// crewshipd_work_ledger_balance_difference readable: the six §10 balance terms
// must PARTITION the state machine. A state missing from the buckets would
// leave the difference permanently non-zero (and the alarm permanently on); a
// state in two buckets would double-count it and hide a real leak of the same
// size. Adding a state to internal/work therefore reds this test rather than
// silently changing what the gauge means.
func TestWorkLedgerBucketsCoverEveryState(t *testing.T) {
	seen := map[string]int{}
	for _, bucket := range workLedgerBuckets {
		for _, st := range bucket.states {
			seen[st]++
		}
	}
	for _, st := range work.AllStates() {
		switch seen[string(st)] {
		case 1:
			delete(seen, string(st))
		case 0:
			t.Errorf("state %q is in no balance bucket: the difference gauge would never return to 0", st)
		default:
			t.Errorf("state %q is in %d balance buckets, want exactly 1", st, seen[string(st)])
		}
	}
	for st := range seen {
		t.Errorf("balance bucket names state %q, which internal/work does not declare", st)
	}
}

// TestCollectWorkMetrics_QueueDepthByClass covers §10's "queue length / oldest
// age podle třídy".
//
// The split by class is the point: §6 reserves capacity for chat that
// background may not borrow, so one aggregate depth would let a background
// backlog hide a starved chat queue — the exact failure the reservation exists
// to prevent.
func TestCollectWorkMetrics_QueueDepthByClass(t *testing.T) {
	s := workFixture(t)

	// Two chat items and three background ones, all still waiting. The oldest
	// background item is accepted a known 10 minutes before the oldest chat
	// one, so the two age series cannot be swapped without the test noticing.
	acceptWork(t, s.db, workBaseTime.Add(-10*time.Minute), work.AcceptRequest{
		WorkspaceID: "ws_queue", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "agent_a",
	})
	acceptWork(t, s.db, workBaseTime.Add(-2*time.Minute), work.AcceptRequest{
		WorkspaceID: "ws_queue", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "agent_b",
	})
	acceptWork(t, s.db, workBaseTime.Add(-1*time.Minute), work.AcceptRequest{
		WorkspaceID: "ws_queue", Source: work.SourceChat, Class: work.ClassChat, AgentID: "agent_a",
	})
	acceptWork(t, s.db, workBaseTime.Add(-30*time.Second), work.AcceptRequest{
		WorkspaceID: "ws_queue", Source: work.SourceChat, Class: work.ClassChat, AgentID: "agent_b",
	})

	// A third background item is in retry_wait. Backoff is not "done": it is
	// accepted work the server has not run, so it counts toward depth.
	retrying := acceptWork(t, s.db, workBaseTime.Add(-5*time.Minute), work.AcceptRequest{
		WorkspaceID: "ws_queue", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "agent_c",
	})
	mustExec(t, s.db, `UPDATE work_items SET state = 'retry_wait' WHERE id = ?`, retrying)

	// And one that already finished, which must NOT be in the queue at all.
	done := acceptWork(t, s.db, workBaseTime.Add(-30*time.Minute), work.AcceptRequest{
		WorkspaceID: "ws_queue", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "agent_d",
	})
	mustExec(t, s.db, `UPDATE work_items SET state = 'succeeded' WHERE id = ?`, done)

	out := renderWorkMetrics(t, s)

	if got := metricValue(t, out, "crewshipd_work_queue_depth", map[string]string{"class": "chat"}); got != 2 {
		t.Errorf("chat queue depth = %v, want 2", got)
	}
	if got := metricValue(t, out, "crewshipd_work_queue_depth", map[string]string{"class": "background"}); got != 3 {
		t.Errorf("background queue depth = %v, want 3 (2 queued + 1 retry_wait, the succeeded one excluded)", got)
	}
	if got := metricValue(t, out, "crewshipd_work_items", map[string]string{"state": "succeeded"}); got != 1 {
		t.Errorf("succeeded work items = %v, want 1", got)
	}

	// Age is measured against wall-clock now, so assert the ordering and a
	// generous floor rather than an exact number.
	chatAge := metricValue(t, out, "crewshipd_work_queue_oldest_age_seconds", map[string]string{"class": "chat"})
	bgAge := metricValue(t, out, "crewshipd_work_queue_oldest_age_seconds", map[string]string{"class": "background"})
	if bgAge <= chatAge {
		t.Errorf("background oldest age %v should exceed chat's %v: its oldest item was accepted 9 minutes earlier", bgAge, chatAge)
	}
	if bgAge-chatAge < 8*60 {
		t.Errorf("age gap = %vs, want at least 480s (10min vs 1min acceptance)", bgAge-chatAge)
	}
}

// TestCollectWorkMetrics_BalanceIdentity covers §10's "Bilance accepted =
// queued/active/waiting/retry/reconciliation/terminal", at zero and then
// deliberately perturbed.
//
// The perturbation is not arbitrary. Inserting a work_items row directly is
// what a producer that bypassed the shared admission path would leave behind,
// which is precisely what I7 forbids — so the gauge moving off zero here is
// the leak it exists to make visible, not a synthetic poke.
func TestCollectWorkMetrics_BalanceIdentity(t *testing.T) {
	s := workFixture(t)

	for i := 0; i < 4; i++ {
		acceptWork(t, s.db, workBaseTime, work.AcceptRequest{
			WorkspaceID: "ws_balance", Source: work.SourceWebhook, Class: work.ClassBackground,
			AgentID: fmt.Sprintf("agent_%d", i),
		})
	}
	// Spread them across the balance terms so the sum is exercising more than
	// one bucket.
	mustExec(t, s.db, `UPDATE work_items SET state = 'running' WHERE id IN (SELECT id FROM work_items LIMIT 1)`)
	mustExec(t, s.db, `UPDATE work_items SET state = 'waiting' WHERE id IN (SELECT id FROM work_items WHERE state = 'queued' LIMIT 1)`)
	mustExec(t, s.db, `UPDATE work_items SET state = 'cancelled' WHERE id IN (SELECT id FROM work_items WHERE state = 'queued' LIMIT 1)`)

	out := renderWorkMetrics(t, s)
	if got := metricValue(t, out, "crewshipd_work_ledger_accepted", nil); got != 4 {
		t.Fatalf("accepted = %v, want 4", got)
	}
	if got := metricValue(t, out, "crewshipd_work_ledger_balance_difference", nil); got != 0 {
		t.Fatalf("balance difference = %v, want 0 for a consistent ledger", got)
	}
	for bucket, want := range map[string]float64{
		"queued": 1, "active": 1, "waiting": 1, "retry": 0, "reconciliation": 0, "terminal": 1,
	} {
		if got := metricValue(t, out, "crewshipd_work_ledger_bucket", map[string]string{"bucket": bucket}); got != want {
			t.Errorf("bucket %q = %v, want %v", bucket, got, want)
		}
	}

	// Now the perturbation: a work item nobody accepted.
	mustExec(t, s.db, `
		INSERT INTO work_items (id, workspace_id, source, state, eligible_at, created_at, updated_at)
		VALUES ('smuggled', 'ws_balance', 'manual', 'queued', ?, ?, ?)`,
		tsformat.Format(workBaseTime), tsformat.Format(workBaseTime), tsformat.Format(workBaseTime))

	out = renderWorkMetrics(t, s)
	if got := metricValue(t, out, "crewshipd_work_ledger_accepted", nil); got != 4 {
		t.Errorf("accepted = %v, want 4 — the smuggled row wrote no acceptance event", got)
	}
	if got := metricValue(t, out, "crewshipd_work_ledger_bucket", map[string]string{"bucket": "queued"}); got != 2 {
		t.Errorf("queued bucket = %v, want 2 after the smuggled insert", got)
	}
	if got := metricValue(t, out, "crewshipd_work_ledger_balance_difference", nil); got != -1 {
		t.Fatalf("balance difference = %v, want -1: one work item exists that no producer accepted (I7)", got)
	}

	// And the other direction: history that outlived its work. Detaching the
	// row's events from the item (rather than deleting the item, which
	// cascades) is the shape a retention bug leaves behind.
	mustExec(t, s.db, `DELETE FROM work_items WHERE id = 'smuggled'`)
	mustExec(t, s.db, `UPDATE work_items SET state = 'expired' WHERE state = 'queued'`)
	mustExec(t, s.db, `DELETE FROM work_items WHERE id IN (SELECT id FROM work_items WHERE state = 'expired' LIMIT 1)`)
	out = renderWorkMetrics(t, s)
	if got := metricValue(t, out, "crewshipd_work_ledger_balance_difference", nil); got != 0 {
		t.Errorf("balance difference = %v, want 0: deleting a work item cascades its events, so both sides drop together", got)
	}
}

// TestCollectWorkMetrics_ReconciliationAndLeaseLoss covers §10's
// "reconciliation" and "lease loss" counters through the real recovery path.
//
// It also pins the coupling metrics_domain_work.go documents: the lease-loss
// series matches on the reason prefix RecoverExpiredLeases writes. Driving the
// real recovery means a reworded reason reds this test instead of silently
// flattening the series to zero forever.
func TestCollectWorkMetrics_ReconciliationAndLeaseLoss(t *testing.T) {
	s := workFixture(t)
	ctx := context.Background()

	// Two items, both claimed. One records a runtime locator, so recovery must
	// park it for reconciliation rather than start a second process (§4, T08);
	// the other never started anything and is safe to requeue.
	withRuntime := acceptWork(t, s.db, workBaseTime, work.AcceptRequest{
		WorkspaceID: "ws_recover", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "agent_live",
	})
	acceptWork(t, s.db, workBaseTime, work.AcceptRequest{
		WorkspaceID: "ws_recover", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "agent_dead",
	})

	claimStore := workStoreAt(s.db, workBaseTime)
	var withRuntimeClaim *work.Claimed
	for i := 0; i < 2; i++ {
		claimed, err := claimStore.Claim(ctx, work.ClaimOptions{LeaseOwner: "dispatcher-1"})
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if claimed.Item.ID == withRuntime {
			withRuntimeClaim = claimed
		}
	}
	if withRuntimeClaim == nil {
		t.Fatalf("the item under test was never claimed")
	}
	// The whole start protocol, in order. StartRunning refuses an attempt that
	// never declared where its runtime would be — without the intent, recovery
	// cannot tell a process that was never created from one we failed to write
	// down, which is the distinction this very test depends on below.
	const locator = "crew-container/run-abc"
	if err := claimStore.MarkStarting(ctx, withRuntime, withRuntimeClaim.RunID,
		withRuntimeClaim.Generation, locator); err != nil {
		t.Fatalf("mark starting: %v", err)
	}
	if err := claimStore.StartRunning(ctx, withRuntime, withRuntimeClaim.RunID,
		withRuntimeClaim.Generation, locator); err != nil {
		t.Fatalf("start running: %v", err)
	}

	// Well past the lease. Nothing heartbeated.
	recoverStore := workStoreAt(s.db, workBaseTime.Add(10*time.Minute))
	outcome, err := recoverStore.RecoverExpiredLeases(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(outcome.Reconciliation) != 1 || len(outcome.Requeued) != 1 {
		t.Fatalf("recovery outcome = %d reconciliation / %d requeued, want 1 / 1", len(outcome.Reconciliation), len(outcome.Requeued))
	}

	out := renderWorkMetrics(t, s)
	if got := metricValue(t, out, "crewshipd_work_items", map[string]string{"state": "needs_reconciliation"}); got != 1 {
		t.Errorf("needs_reconciliation work items = %v, want 1", got)
	}
	if got := metricValue(t, out, "crewshipd_work_reconciliation_total", nil); got != 1 {
		t.Errorf("reconciliation total = %v, want 1", got)
	}
	if got := metricValue(t, out, "crewshipd_work_lease_losses_total", map[string]string{"outcome": "reconciliation"}); got != 1 {
		t.Errorf("lease losses (reconciliation) = %v, want 1", got)
	}
	if got := metricValue(t, out, "crewshipd_work_lease_losses_total", map[string]string{"outcome": "requeued"}); got != 1 {
		t.Errorf("lease losses (requeued) = %v, want 1", got)
	}
	if got := metricValue(t, out, "crewshipd_work_attempts_total", nil); got != 2 {
		t.Errorf("attempts = %v, want 2", got)
	}
	// needs_reconciliation holds its execution slot on purpose, so it is still
	// in the live set — that is the property that stops a second runtime from
	// starting for work whose first was never confirmed stopped.
	if got := metricValue(t, out, "crewshipd_work_live", map[string]string{"class": "background"}); got != 1 {
		t.Errorf("live background = %v, want 1: needs_reconciliation still holds a slot", got)
	}
	// The start-latency series has one real sample: the claim that reached
	// `running`. The other attempt never confirmed a runtime and must not
	// contribute a fabricated zero.
	if got := metricValue(t, out, "crewshipd_work_start_latency_sample_count", nil); got != 1 {
		t.Errorf("start latency samples = %v, want 1", got)
	}
}

// TestCollectWorkMetrics_NoIDShapedLabels is the label-hygiene rule §10 states
// outright: "Metriky exportovat bez raw payloadu, credentials a
// high-cardinality run IDs v labels. IDs patří do strukturovaného auditu."
//
// It is an ALLOW-list, not a search for id-looking strings. A deny-list only
// catches the id shapes somebody thought of, and the failure this guards
// against is a future collector adding `workspace_id` or `agent_id` "just for
// this one panel" — which is unbounded cardinality and would take a scraper
// down long after the review that would have caught it.
//
// The fixture deliberately seeds real CUID work ids, run ids, workspace ids,
// agent ids and session ids first, so there is something to leak.
func TestCollectWorkMetrics_NoIDShapedLabels(t *testing.T) {
	s := workFixture(t)
	ctx := context.Background()

	id := acceptWork(t, s.db, workBaseTime, work.AcceptRequest{
		WorkspaceID: "ws_2f8c1a9b4e", Source: work.SourceChat, Class: work.ClassChat,
		AgentID: "cmf3k2j10000abcd1234efgh", CrewID: "crew_8812", SessionID: "sess_9f2c",
	})
	claimed, err := workStoreAt(s.db, workBaseTime).Claim(ctx, work.ClaimOptions{LeaseOwner: "dispatcher-1"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.Item.ID != id {
		t.Fatalf("claimed %q, want %q", claimed.Item.ID, id)
	}
	mustExec(t, s.db, `
		INSERT INTO webhook_deliveries (id, workspace_id, endpoint_id, endpoint_kind, profile,
			source_delivery_id, body_sha256, filter_decision, received_at, dedup_expires_at)
		VALUES ('del_a1', 'ws_2f8c1a9b4e', 'ep_77', 'agent', 'github', 'gh-delivery-9', 'abc', 'accepted', ?, ?)`,
		tsformat.Format(workBaseTime), tsformat.Format(workBaseTime.Add(time.Hour)))

	out := renderWorkMetrics(t, s)

	// Every label value this block is allowed to emit, from the closed sets it
	// declares. hostname is handled separately: it is operator-chosen, so it
	// is checked for equality with what the caller passed rather than
	// membership.
	allowed := map[string]bool{"0.5": true, "0.95": true}
	for _, group := range [][]string{
		workStateSet, workClassSet, workDeliveryDecisionSet,
		memoryMutationStateSet, workLeaseLossOutcomeSet,
	} {
		for _, v := range group {
			allowed[v] = true
		}
	}
	for _, bucket := range workLedgerBuckets {
		allowed[bucket.name] = true
	}
	for _, r := range work.RejectReasons {
		allowed[string(r)] = true
	}

	var lines int
	for _, line := range strings.Split(out, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		labels, _, ok := parsePromLine(line)
		if !ok {
			t.Fatalf("unparsable exposition line %q", line)
		}
		lines++
		for k, v := range labels {
			if k == "hostname" {
				if v != workMetricsHostname {
					t.Errorf("hostname label = %q, want %q (line %q)", v, workMetricsHostname, line)
				}
				continue
			}
			if k == "id" || strings.HasSuffix(k, "_id") {
				t.Errorf("label key %q is an identifier dimension — §10 puts ids in the journal, never in a label (line %q)", k, line)
			}
			if !allowed[v] {
				t.Errorf("label %s=%q is not a value of any closed set this block declares; "+
					"an unbounded label value would multiply the series set per work item (line %q)", k, v, line)
			}
		}
	}
	if lines == 0 {
		t.Fatal("the work block rendered no sample lines at all")
	}

	// Belt and braces: the ids that were seeded must not appear anywhere in
	// the text, not even inside a HELP string.
	for _, secret := range []string{id, claimed.RunID, "ws_2f8c1a9b4e", "cmf3k2j10000abcd1234efgh", "sess_9f2c", "del_a1", "gh-delivery-9"} {
		if strings.Contains(out, secret) {
			t.Errorf("exposition contains the identifier %q", secret)
		}
	}
}

// TestCollectWorkMetrics_CapacityInvariant covers §10's "Kapacitní invariant"
// row. The gauge is a canary, not a load signal: §6's caps are enforced
// atomically inside the claim transaction, so a non-zero value means admission
// did not hold.
func TestCollectWorkMetrics_CapacityInvariant(t *testing.T) {
	s := workFixture(t)
	limits := work.DefaultLimits()

	if got := metricValue(t, renderWorkMetrics(t, s), "crewshipd_work_capacity_violations", nil); got != 0 {
		t.Fatalf("empty ledger reports %v capacity violations, want 0", got)
	}

	// One agent over its per-agent background cap, written directly because
	// Claim would (correctly) refuse to produce this state.
	for i := 0; i <= limits.AgentBackground; i++ {
		id := acceptWork(t, s.db, workBaseTime, work.AcceptRequest{
			WorkspaceID: "ws_cap", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "agent_over",
		})
		mustExec(t, s.db, `UPDATE work_items SET state = 'running' WHERE id = ?`, id)
	}

	out := renderWorkMetrics(t, s)
	got := metricValue(t, out, "crewshipd_work_capacity_violations", nil)
	if got < 1 {
		t.Errorf("capacity violations = %v, want at least 1 with %d background runs on one agent (cap %d)",
			got, limits.AgentBackground+1, limits.AgentBackground)
	}
	if live := metricValue(t, out, "crewshipd_work_live", map[string]string{"class": "background"}); live != float64(limits.AgentBackground+1) {
		t.Errorf("live background = %v, want %d", live, limits.AgentBackground+1)
	}
	// The scrape-time observation must also land in the monotonic counter, or
	// a violation that heals between two scrapes leaves no trace at all.
	if total := metricValue(t, out, "crewshipd_work_capacity_violations_total", nil); total < 1 {
		t.Errorf("capacity violations total = %v, want at least 1 after an observed violation", total)
	}
}

// TestCollectWorkMetrics_ProcessCounters covers the half of §10's
// accept/reject/duplicate row that no table can answer.
//
// Assertions are on DELTAS. The counters are process-global by design (the
// same shape as crewshipd_credential_audit_dropped_total), so another test in
// this binary may have incremented them; an absolute assertion would be a
// shuffle-order flake waiting to happen.
func TestCollectWorkMetrics_ProcessCounters(t *testing.T) {
	s := workFixture(t)

	before := renderWorkMetrics(t, s)
	baseDup := metricValue(t, before, "crewshipd_work_duplicate_deliveries_total", nil)
	baseSig := metricValue(t, before, "crewshipd_work_rejected_total", map[string]string{"reason": "signature"})
	baseOther := metricValue(t, before, "crewshipd_work_rejected_total", map[string]string{"reason": "other"})
	baseCleanup := metricValue(t, before, "crewshipd_work_cleanup_failures_total", nil)

	work.RecordDuplicate()
	work.RecordDuplicate()
	work.RecordRejected(work.RejectSignature)
	work.RecordRejected("something nobody declared") // must fold into `other`
	work.RecordCleanupFailure()

	after := renderWorkMetrics(t, s)
	if got := metricValue(t, after, "crewshipd_work_duplicate_deliveries_total", nil) - baseDup; got != 2 {
		t.Errorf("duplicate delta = %v, want 2", got)
	}
	if got := metricValue(t, after, "crewshipd_work_rejected_total", map[string]string{"reason": "signature"}) - baseSig; got != 1 {
		t.Errorf("signature reject delta = %v, want 1", got)
	}
	if got := metricValue(t, after, "crewshipd_work_rejected_total", map[string]string{"reason": "other"}) - baseOther; got != 1 {
		t.Errorf("other reject delta = %v, want 1: an undeclared reason must fold, never mint a label value", got)
	}
	if got := metricValue(t, after, "crewshipd_work_cleanup_failures_total", nil) - baseCleanup; got != 1 {
		t.Errorf("cleanup failure delta = %v, want 1", got)
	}
}

// TestCollectWorkMetrics_EmptyLedgerZeroFills pins this block's half of the
// zero-filled-closed-set convention: a migrated but empty database must still
// emit every bounded series, so an absent() alert and a dashboard have a
// stable series set from the very first scrape.
//
// The exception is the three latency families, which must be ABSENT rather
// than 0 — §10 requires functionality that does not exist to be marked N/A,
// "nikoli nulou", and an omitted series is how this format says that.
func TestCollectWorkMetrics_EmptyLedgerZeroFills(t *testing.T) {
	// A new database does not reset the process-wide acceptance sampler.
	// Exercise the first-scrape contract in a fresh process so another test's
	// observations cannot turn an empty database into an empty process.
	const childEnv = "WORK_METRICS_EMPTY_CHILD"
	if os.Getenv(childEnv) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCollectWorkMetrics_EmptyLedgerZeroFills$", "-test.count=1", "-test.timeout=2m")
		// Reuse a fresh copy of the parent process's migrated template. Only
		// globals need isolation; rebuilding every migration under -race in
		// the child consumed its whole timeout before the first assertion.
		db := testutil.MigratedDB(t)
		cmd.Env = append(os.Environ(), childEnv+"="+db.Path())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fresh-process metrics: %v\n%s", err, out)
		}
		return
	}
	db, err := database.Open("file:" + os.Getenv(childEnv))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Server{db: db.DB, logger: logging.New("error", "json", nil)}
	out := renderWorkMetrics(t, s)

	for _, state := range workStateSet {
		if got := metricValue(t, out, "crewshipd_work_items", map[string]string{"state": state}); got != 0 {
			t.Errorf("empty ledger: work items[%s] = %v, want 0", state, got)
		}
	}
	for _, class := range workClassSet {
		if got := metricValue(t, out, "crewshipd_work_queue_depth", map[string]string{"class": class}); got != 0 {
			t.Errorf("empty ledger: queue depth[%s] = %v, want 0", class, got)
		}
		if got := metricValue(t, out, "crewshipd_work_queue_oldest_age_seconds", map[string]string{"class": class}); got != 0 {
			t.Errorf("empty ledger: queue age[%s] = %v, want 0", class, got)
		}
	}
	for _, decision := range workDeliveryDecisionSet {
		metricValue(t, out, "crewshipd_work_deliveries", map[string]string{"decision": decision})
	}
	for _, state := range memoryMutationStateSet {
		metricValue(t, out, "crewshipd_memory_mutations", map[string]string{"state": state})
	}
	if got := metricValue(t, out, "crewshipd_work_ledger_balance_difference", nil); got != 0 {
		t.Errorf("empty ledger: balance difference = %v, want 0", got)
	}

	for _, family := range []string{
		"crewshipd_work_acceptance_latency_seconds",
		"crewshipd_work_queue_age_seconds",
		"crewshipd_work_start_latency_seconds",
	} {
		if !strings.Contains(out, "# TYPE "+family+" gauge") {
			t.Errorf("family %s has no TYPE header — a dashboard needs a stable name to point at", family)
		}
		if strings.Contains(out, family+"{") {
			t.Errorf("family %s emitted a sample with no observations: §10 wants N/A, not a fabricated 0", family)
		}
		if got := metricValue(t, out, family[:len(family)-len("_seconds")]+"_sample_count", nil); got != 0 {
			t.Errorf("%s sample count = %v, want 0", family, got)
		}
	}

	// The WAL series are always present: a missing -wal sidecar is genuinely
	// 0 bytes, not an unmeasured value.
	metricValue(t, out, "crewshipd_wal_bytes", nil)
	metricValue(t, out, "crewshipd_wal_autocheckpoint_frames", nil)
}

// TestCollectWorkMetrics_MemoryConflicts covers §10's "memory conflict"
// counter. `conflicted` is the state §8's recovery reaches when the file on
// disk matches neither hash — a third party owns those bytes, so nothing is
// overwritten and the count only falls when a human imports the file.
func TestCollectWorkMetrics_MemoryConflicts(t *testing.T) {
	s := workFixture(t)
	insert := func(id, state string) {
		mustExec(t, s.db, `
			INSERT INTO memory_mutations (id, workspace_id, scope, path, canonical_path,
				operation_id, request_sha256, op, new_revision, target_sha256, state, recorded_at)
			VALUES (?, 'ws_mem', 'own', 'AGENT.md', '/srv/memory/AGENT.md',
				?, 'sha', 'replace', 1, 'target', ?, ?)`,
			id, "op_"+id, state, tsformat.Format(workBaseTime))
	}
	insert("m1", "conflicted")
	insert("m2", "conflicted")
	insert("m3", "confirmed")
	insert("m4", "intent")

	out := renderWorkMetrics(t, s)
	for state, want := range map[string]float64{"conflicted": 2, "confirmed": 1, "intent": 1, "renamed": 0} {
		if got := metricValue(t, out, "crewshipd_memory_mutations", map[string]string{"state": state}); got != want {
			t.Errorf("memory mutations[%s] = %v, want %v", state, got, want)
		}
	}
}

// TestCollectWorkMetrics_AcceptanceLatencyFromProcessSamples covers the one
// §10 timing row with no column behind it. The interval is measured by the
// handler holding the request, so the only thing this test can pin is that a
// recorded sample reaches the exposition with the right value.
func TestCollectWorkMetrics_AcceptanceLatencyFromProcessSamples(t *testing.T) {
	s := workFixture(t)

	before := metricValue(t, renderWorkMetrics(t, s), "crewshipd_work_acceptance_latency_sample_count", nil)
	work.RecordAcceptanceLatency(250 * time.Millisecond)

	out := renderWorkMetrics(t, s)
	if got := metricValue(t, out, "crewshipd_work_acceptance_latency_sample_count", nil); got != before+1 {
		t.Fatalf("acceptance sample count = %v, want %v", got, before+1)
	}
	// p95 over a window that may already hold samples from another test in
	// this binary: assert the series now EXISTS and reports a real observation
	// rather than a specific quantile.
	p95 := metricValue(t, out, "crewshipd_work_acceptance_latency_seconds", map[string]string{"quantile": "0.95"})
	if p95 <= 0 {
		t.Errorf("acceptance p95 = %v, want a positive observed interval", p95)
	}
}
