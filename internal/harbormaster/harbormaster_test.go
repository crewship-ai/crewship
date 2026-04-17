package harbormaster

import (
	"context"
	"database/sql"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// schemaSQL mirrors migration 52 for approvals_queue plus the workspaces
// FK target. Kept inline so the unit test is self-contained — pulling in
// the whole migrate package would be slower and would couple this
// package's tests to migration ordering.
const schemaSQL = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
INSERT INTO workspaces (id) VALUES ('ws_test');

CREATE TABLE approvals_queue (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    agent_id TEXT,
    mission_id TEXT,
    requested_by TEXT NOT NULL,
    kind TEXT NOT NULL,
    reason TEXT NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','approved','denied','timeout','cancelled')),
    decided_by TEXT,
    decided_at TEXT,
    decision_comment TEXT,
    timeout_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_approvals_status ON approvals_queue(status, timeout_at);
CREATE INDEX idx_approvals_ws ON approvals_queue(workspace_id, created_at DESC);
CREATE INDEX idx_approvals_agent ON approvals_queue(agent_id, status);
`

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), schemaSQL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// recorderEmitter captures journal Emit calls so tests can assert on them
// without standing up the journal Writer. It satisfies journal.Emitter.
type recorderEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (r *recorderEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e.ID == "" {
		e.ID = "rec_" + e.Summary
	}
	r.entries = append(r.entries, e)
	return e.ID, nil
}

func (r *recorderEmitter) Flush(_ context.Context) error { return nil }

func (r *recorderEmitter) typesEmitted() []journal.EntryType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]journal.EntryType, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.Type)
	}
	return out
}

func (r *recorderEmitter) hasType(want journal.EntryType) bool {
	for _, t := range r.typesEmitted() {
		if t == want {
			return true
		}
	}
	return false
}

func newReq() Request {
	return Request{
		WorkspaceID: "ws_test",
		AgentID:     "agent_1",
		RequestedBy: "agent_1",
		Kind:        KindToolCall,
		Reason:      "needs human eyes",
		Payload:     map[string]any{"tool": "deploy_prod"},
	}
}

func TestEnqueueRoundtrip(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	rec := &recorderEmitter{}
	ctx := context.Background()

	id, err := Enqueue(ctx, db, rec, newReq())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("expected id")
	}
	if !rec.hasType(journal.EntryApprovalRequest) {
		t.Fatalf("expected approval.request entry, got %v", rec.typesEmitted())
	}

	got, err := Get(ctx, db, "ws_test", id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected row")
	}
	if got.Status != StatusPending {
		t.Errorf("status: got %q want pending", got.Status)
	}
	if got.TimeoutAt == nil || got.TimeoutAt.Before(time.Now()) {
		t.Errorf("timeout_at not set in future: %v", got.TimeoutAt)
	}
	if got.Payload["tool"] != "deploy_prod" {
		t.Errorf("payload roundtrip: %v", got.Payload)
	}
}

func TestDecideIdempotency(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	rec := &recorderEmitter{}
	ctx := context.Background()
	id, err := Enqueue(ctx, db, rec, newReq())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if err := Decide(ctx, db, rec, "ws_test", id, StatusApproved, "alice", "looks good"); err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if !rec.hasType(journal.EntryApprovalGranted) {
		t.Fatalf("expected granted entry, got %v", rec.typesEmitted())
	}

	// Second decide on already-resolved row must error with ErrNotPending.
	err = Decide(ctx, db, rec, "ws_test", id, StatusDenied, "bob", "too late")
	if err != ErrNotPending {
		t.Errorf("second decide: got %v want ErrNotPending", err)
	}

	// Bad status must reject before touching DB.
	if err := Decide(ctx, db, rec, "ws_test", id, StatusPending, "alice", ""); err != ErrBadStatus {
		t.Errorf("bad status: got %v want ErrBadStatus", err)
	}

	// Unknown id is ErrNotFound.
	if err := Decide(ctx, db, rec, "ws_test", "ap_nope", StatusApproved, "alice", ""); err != ErrNotFound {
		t.Errorf("missing: got %v want ErrNotFound", err)
	}
}

func TestSweepTimeouts(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	rec := &recorderEmitter{}
	ctx := context.Background()
	req := newReq()
	req.TimeoutSecs = 60
	id, err := Enqueue(ctx, db, rec, req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Fast-forward by rewriting timeout_at to the past — equivalent to a
	// fake clock for test purposes without coupling to a clock package.
	past := time.Now().UTC().Add(-time.Minute).Format(timeFmt)
	if _, err := db.ExecContext(ctx, `UPDATE approvals_queue SET timeout_at = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("rewrite timeout: %v", err)
	}

	n, err := SweepTimeouts(ctx, db, rec)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("rows affected: got %d want 1", n)
	}
	if !rec.hasType(journal.EntryApprovalTimeout) {
		t.Fatalf("expected timeout entry, got %v", rec.typesEmitted())
	}

	got, err := Get(ctx, db, "ws_test", id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusTimeout {
		t.Errorf("status: got %q want timeout", got.Status)
	}

	// Re-running the sweep is a no-op (idempotent).
	n2, err := SweepTimeouts(ctx, db, rec)
	if err != nil {
		t.Fatalf("sweep2: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second sweep: got %d want 0", n2)
	}
}

func TestEvaluatorDefaultRules(t *testing.T) {
	ev := NewEvaluatorWithDefaults()
	ctx := context.Background()

	// 1. Destructive tool name.
	required, _, kind := ev.Evaluate(ctx, "deploy_to_prod", nil)
	if !required || kind != KindDestructiveOp {
		t.Errorf("destructive: required=%v kind=%v", required, kind)
	}

	required, _, kind = ev.Evaluate(ctx, "delete_user", nil)
	if !required || kind != KindDestructiveOp {
		t.Errorf("delete_*: required=%v kind=%v", required, kind)
	}

	// 2. Cost threshold.
	required, _, kind = ev.Evaluate(ctx, "noop", map[string]any{"cost_estimate_usd": 25.0})
	if !required || kind != KindCostThreshold {
		t.Errorf("cost: required=%v kind=%v", required, kind)
	}
	required, _, _ = ev.Evaluate(ctx, "noop", map[string]any{"cost_estimate_usd": 1.5})
	if required {
		t.Errorf("cheap call should not require approval")
	}

	// 3. Target environment.
	required, _, kind = ev.Evaluate(ctx, "noop", map[string]any{"host": "api.production.example.com"})
	if !required || kind != KindTargetEnvironment {
		t.Errorf("target prod: required=%v kind=%v", required, kind)
	}
	required, _, _ = ev.Evaluate(ctx, "noop", map[string]any{"host": "api.staging.example.com"})
	if required {
		t.Errorf("staging host should not require approval")
	}

	// 4. No match falls through.
	required, _, _ = ev.Evaluate(ctx, "harmless_get", map[string]any{"x": 1})
	if required {
		t.Errorf("harmless tool should not require approval")
	}

	// 5. Custom rule extension.
	custom := RuleMatcher{
		Name:        "block-curl",
		ToolPattern: regexp.MustCompile("^curl$"),
		MapsToKind:  KindCustom,
	}
	ev2 := NewEvaluator(custom)
	required, _, kind = ev2.Evaluate(ctx, "curl", nil)
	if !required || kind != KindCustom {
		t.Errorf("custom rule: required=%v kind=%v", required, kind)
	}
}

func TestGateAsyncReturnsPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ev := NewEvaluatorWithDefaults()
	rec := &recorderEmitter{}
	ctx := context.Background()

	dec, err := Gate(ctx, db, rec, ev, GateInput{
		Mode:        ModeAsync,
		Tool:        "deploy_prod",
		WorkspaceID: "ws_test",
		AgentID:     "agent_1",
		RequestedBy: "agent_1",
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.Pending || dec.RequestID == "" {
		t.Fatalf("expected Pending+RequestID, got %+v", dec)
	}
	if dec.Kind != KindDestructiveOp {
		t.Errorf("kind: got %v want destructive_op", dec.Kind)
	}

	// No-rule call returns NotGated+Approved without enqueueing.
	dec2, err := Gate(ctx, db, rec, ev, GateInput{
		Mode: ModeAsync, Tool: "harmless", WorkspaceID: "ws_test", RequestedBy: "agent_1",
	})
	if err != nil {
		t.Fatalf("gate2: %v", err)
	}
	if !dec2.Approved || !dec2.NotGated || dec2.RequestID != "" {
		t.Errorf("expected NotGated approval, got %+v", dec2)
	}
}

func TestGateSyncPolls(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ev := NewEvaluatorWithDefaults()
	rec := &recorderEmitter{}
	ctx := context.Background()

	// Approve the row out-of-band shortly after Gate starts polling.
	approveDone := make(chan struct{})
	go func() {
		// Wait a beat for Gate to enqueue + start polling, then look up
		// the row and decide.
		time.Sleep(50 * time.Millisecond)
		rows, err := List(ctx, db, "ws_test", StatusPending, 10)
		if err != nil || len(rows) == 0 {
			close(approveDone)
			return
		}
		_ = Decide(ctx, db, rec, "ws_test", rows[0].ID, StatusApproved, "alice", "ok")
		close(approveDone)
	}()

	dec, err := Gate(ctx, db, rec, ev, GateInput{
		Mode:         ModeSync,
		Tool:         "deploy_prod",
		WorkspaceID:  "ws_test",
		AgentID:      "agent_1",
		RequestedBy:  "agent_1",
		PollInterval: 20 * time.Millisecond,
		TimeoutSecs:  5,
	})
	<-approveDone
	if err != nil {
		t.Fatalf("gate sync: %v", err)
	}
	if !dec.Approved || dec.DecidedBy != "alice" {
		t.Errorf("expected approved by alice, got %+v", dec)
	}
}

func TestGateSyncTimeout(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ev := NewEvaluatorWithDefaults()
	rec := &recorderEmitter{}
	ctx := context.Background()

	start := time.Now()
	dec, err := Gate(ctx, db, rec, ev, GateInput{
		Mode:         ModeSync,
		Tool:         "deploy_prod",
		WorkspaceID:  "ws_test",
		AgentID:      "agent_1",
		RequestedBy:  "agent_1",
		PollInterval: 20 * time.Millisecond,
		TimeoutSecs:  1, // hit the client-side deadline quickly
	})
	if err != nil {
		t.Fatalf("gate sync: %v", err)
	}
	if !dec.TimedOut {
		t.Errorf("expected TimedOut, got %+v", dec)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
	if !rec.hasType(journal.EntryApprovalTimeout) {
		t.Errorf("expected timeout journal entry, got %v", rec.typesEmitted())
	}
}

func TestGateModeNoneSkipsRules(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ev := NewEvaluatorWithDefaults()
	dec, err := Gate(context.Background(), db, nil, ev, GateInput{
		Mode:        ModeNone,
		Tool:        "deploy_prod", // would normally trigger
		WorkspaceID: "ws_test",
		RequestedBy: "agent_1",
	})
	if err != nil {
		t.Fatalf("gate none: %v", err)
	}
	if !dec.Approved || !dec.NotGated {
		t.Errorf("ModeNone should approve unconditionally, got %+v", dec)
	}
}

func TestCancelPending(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	rec := &recorderEmitter{}
	ctx := context.Background()
	id, err := Enqueue(ctx, db, rec, newReq())
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := Cancel(ctx, db, rec, "ws_test", id, "agent aborted"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ := Get(ctx, db, "ws_test", id)
	if got.Status != StatusCancelled {
		t.Errorf("status: got %q want cancelled", got.Status)
	}
	// Cancelling again is ErrNotPending.
	if err := Cancel(ctx, db, rec, "ws_test", id, "again"); err != ErrNotPending {
		t.Errorf("re-cancel: got %v want ErrNotPending", err)
	}
}

func TestListFiltersByStatus(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	rec := &recorderEmitter{}
	ctx := context.Background()

	id1, _ := Enqueue(ctx, db, rec, newReq())
	id2, _ := Enqueue(ctx, db, rec, newReq())
	_ = Decide(ctx, db, rec, "ws_test", id1, StatusApproved, "alice", "")

	pending, err := List(ctx, db, "ws_test", StatusPending, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != id2 {
		t.Errorf("pending list: %+v", pending)
	}

	all, err := List(ctx, db, "ws_test", "", 10)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("all list len: %d", len(all))
	}
}
