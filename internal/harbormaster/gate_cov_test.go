package harbormaster

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	_ "modernc.org/sqlite"
)

// openGateTestDB extends the canonical schema with gate_reward_history so
// auto-tuning paths can be exercised, and pins the pool to one connection
// (":memory:" gives every new connection its own database).
func openGateTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE gate_reward_history (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
			tool_name TEXT NOT NULL, args_hash TEXT NOT NULL,
			outcome TEXT NOT NULL, decided_by TEXT,
			decided_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			request_id TEXT);`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// hookEmitter runs a side-effect on the first Emit of a matching entry type.
// Gate's flow is enqueue → journal emit → poll, so a hook on the
// approval.requested emit lets a test mutate the row (or the environment)
// deterministically before the first poll runs.
type hookEmitter struct {
	mu      sync.Mutex
	hookOn  journal.EntryType
	hook    func()
	fired   bool
	emitErr error // returned for every Emit when set
	entries []journal.Entry
}

func (h *hookEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	h.mu.Lock()
	h.entries = append(h.entries, e)
	shouldFire := !h.fired && e.Type == h.hookOn && h.hook != nil
	if shouldFire {
		h.fired = true
	}
	err := h.emitErr
	h.mu.Unlock()
	if shouldFire {
		h.hook()
	}
	return "hook_" + string(e.Type), err
}

func (h *hookEmitter) Flush(_ context.Context) error { return nil }

func (h *hookEmitter) hasType(want journal.EntryType) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.entries {
		if e.Type == want {
			return true
		}
	}
	return false
}

func TestGate_NilEvaluatorIsNotGated(t *testing.T) {
	db := openGateTestDB(t)
	dec, err := Gate(context.Background(), db, nil, nil, GateInput{
		Mode: ModeSync, Tool: "deploy_prod", WorkspaceID: "ws_test", RequestedBy: "agent_1",
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.NotGated || !dec.Approved {
		t.Errorf("nil evaluator should short-circuit to NotGated+Approved, got %+v", dec)
	}
}

func TestGate_EnqueueErrorPropagates(t *testing.T) {
	db := openGateTestDB(t)
	ev := NewEvaluatorWithDefaults()
	_, err := Gate(context.Background(), db, nil, ev, GateInput{
		Mode: ModeAsync, Tool: "deploy_prod", WorkspaceID: "ws_test",
		RequestedBy: "", // Enqueue rejects empty requested_by
	})
	if err == nil {
		t.Fatal("expected enqueue error")
	}
	if !strings.Contains(err.Error(), "gate enqueue") {
		t.Errorf("error %q should wrap gate enqueue", err)
	}
}

// Ten unanimous approvals downgrade sync → async: the gate returns Pending
// instead of blocking, and the auto-tune journal entry is emitted.
func TestGate_AutoTuneSyncDowngradesToAsync(t *testing.T) {
	db := openGateTestDB(t)
	ctx := context.Background()
	ev := NewEvaluatorWithDefaults()

	args := map[string]any{"target": "prod-cluster"}
	for i := 0; i < RewardHistorySize/2; i++ {
		if err := RecordOutcome(ctx, db, "ws_test", "deploy_prod", args, OutcomeApproved,
			"alice", fmt.Sprintf("req_%d", i)); err != nil {
			t.Fatalf("seed reward history: %v", err)
		}
	}

	rec := &hookEmitter{}
	dec, err := Gate(ctx, db, rec, ev, GateInput{
		Mode:        ModeSync, // would block; auto-tune must flip it to async
		Tool:        "deploy_prod",
		Args:        args,
		WorkspaceID: "ws_test",
		AgentID:     "agent_1",
		RequestedBy: "agent_1",
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.Pending {
		t.Fatalf("expected auto-tuned async Pending decision, got %+v", dec)
	}
	if !rec.hasType(journal.EntryType("keeper.rule_auto_tuned")) {
		t.Errorf("expected keeper.rule_auto_tuned journal entry, got %v", rec.entries)
	}
}

// Terminal states resolved before the first poll must map onto the right
// Decision fields, including the fail-closed handling of a vanished row.
func TestGate_SyncFirstPollTerminalStates(t *testing.T) {
	cases := []struct {
		name     string
		mutate   string // SQL run via the journal hook, "" = delete row
		want     func(t *testing.T, dec Decision)
		wantWait bool
	}{
		{
			name:   "denied",
			mutate: `UPDATE approvals_queue SET status='denied', decided_by='bob', decision_comment='no way' WHERE status='pending'`,
			want: func(t *testing.T, dec Decision) {
				if !dec.Denied || dec.Status != StatusDenied {
					t.Errorf("expected Denied, got %+v", dec)
				}
				if dec.DecidedBy != "bob" || dec.Comment != "no way" {
					t.Errorf("decided_by/comment not carried: %+v", dec)
				}
			},
		},
		{
			name:   "timeout flipped by sweeper",
			mutate: `UPDATE approvals_queue SET status='timeout' WHERE status='pending'`,
			want: func(t *testing.T, dec Decision) {
				if !dec.TimedOut || dec.Status != StatusTimeout {
					t.Errorf("expected TimedOut, got %+v", dec)
				}
			},
		},
		{
			name:   "cancelled",
			mutate: `UPDATE approvals_queue SET status='cancelled', decision_comment='agent gone' WHERE status='pending'`,
			want: func(t *testing.T, dec Decision) {
				if !dec.Denied || dec.Status != StatusCancelled {
					t.Errorf("expected Denied via cancelled, got %+v", dec)
				}
			},
		},
		{
			name:   "row vanished fails closed",
			mutate: `DELETE FROM approvals_queue WHERE status='pending'`,
			want: func(t *testing.T, dec Decision) {
				if !dec.Denied || dec.Status != StatusDenied {
					t.Errorf("expected fail-closed Denied, got %+v", dec)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			db := openGateTestDB(t)
			ctx := context.Background()
			rec := &hookEmitter{hookOn: journal.EntryApprovalRequest}
			rec.hook = func() {
				if _, err := db.Exec(tc.mutate); err != nil {
					t.Errorf("mutate: %v", err)
				}
			}

			// Default PollInterval + default TimeoutSecs paths are also
			// exercised here: the fast first poll resolves immediately so
			// neither the 1s ticker nor the 1h deadline ever fires.
			dec, err := Gate(ctx, db, rec, NewEvaluatorWithDefaults(), GateInput{
				Mode:        ModeSync,
				Tool:        "deploy_prod",
				WorkspaceID: "ws_test",
				AgentID:     "agent_1",
				RequestedBy: "agent_1",
			})
			if err != nil {
				t.Fatalf("gate: %v", err)
			}
			if dec.RequestID == "" {
				t.Error("decision should carry the request id")
			}
			tc.want(t, dec)
		})
	}
}

func TestGate_SyncPreparePollErrorWhenDBCloses(t *testing.T) {
	db := openGateTestDB(t)
	rec := &hookEmitter{hookOn: journal.EntryApprovalRequest, hook: func() { db.Close() }}

	_, err := Gate(context.Background(), db, rec, NewEvaluatorWithDefaults(), GateInput{
		Mode:        ModeSync,
		Tool:        "deploy_prod",
		WorkspaceID: "ws_test",
		RequestedBy: "agent_1",
	})
	if err == nil {
		t.Fatal("expected prepare error after db close")
	}
	if !strings.Contains(err.Error(), "prepare poll") {
		t.Errorf("error %q should wrap prepare poll", err)
	}
}

func TestGate_SyncPollErrorOnDroppedTable(t *testing.T) {
	db := openGateTestDB(t)
	rec := &hookEmitter{hookOn: journal.EntryApprovalRequest, hook: func() {
		if _, err := db.Exec(`ALTER TABLE approvals_queue RENAME TO gone`); err != nil {
			t.Errorf("rename: %v", err)
		}
	}}

	_, err := Gate(context.Background(), db, rec, NewEvaluatorWithDefaults(), GateInput{
		Mode:        ModeSync,
		Tool:        "deploy_prod",
		WorkspaceID: "ws_test",
		RequestedBy: "agent_1",
	})
	if err == nil {
		t.Fatal("expected poll error after table rename")
	}
	if !strings.Contains(err.Error(), "poll") {
		t.Errorf("error %q should wrap the poll failure", err)
	}
}

func TestGate_SyncCtxCancelledWhilePolling(t *testing.T) {
	db := openGateTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Cancel shortly AFTER the fast first poll has seen the row pending, so
	// the gate is parked in the select when the context dies.
	rec := &hookEmitter{hookOn: journal.EntryApprovalRequest, hook: func() {
		time.AfterFunc(50*time.Millisecond, cancel)
	}}

	_, err := Gate(ctx, db, rec, NewEvaluatorWithDefaults(), GateInput{
		Mode:         ModeSync,
		Tool:         "deploy_prod",
		WorkspaceID:  "ws_test",
		RequestedBy:  "agent_1",
		PollInterval: time.Hour, // ticker must not win the select
		TimeoutSecs:  3600,
	})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// A decision that lands between the last poll and the deadline fire must be
// returned as-is instead of being misreported as a timeout.
func TestGate_DeadlineRaceReturnsActualDecision(t *testing.T) {
	db := openGateTestDB(t)
	ctx := context.Background()
	rec := &hookEmitter{}

	var approveOnce sync.Once
	hooked := &hookEmitter{hookOn: journal.EntryApprovalRequest, hook: func() {
		// Approve out-of-band well before the 1s client deadline. The huge
		// PollInterval guarantees no poll observes it first — only the
		// deadline-fire re-check can.
		time.AfterFunc(100*time.Millisecond, func() {
			approveOnce.Do(func() {
				rows, err := List(ctx, db, "ws_test", StatusPending, 10)
				if err != nil || len(rows) == 0 {
					t.Errorf("list pending: %v (%d rows)", err, len(rows))
					return
				}
				if err := Decide(ctx, db, rec, "ws_test", rows[0].ID, StatusApproved, "alice", "late"); err != nil {
					t.Errorf("decide: %v", err)
				}
			})
		})
	}}

	dec, err := Gate(ctx, db, hooked, NewEvaluatorWithDefaults(), GateInput{
		Mode:         ModeSync,
		Tool:         "deploy_prod",
		WorkspaceID:  "ws_test",
		RequestedBy:  "agent_1",
		PollInterval: time.Hour,
		TimeoutSecs:  1,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.Approved || dec.TimedOut {
		t.Fatalf("expected the real approval, not a timeout: %+v", dec)
	}
	if dec.DecidedBy != "alice" {
		t.Errorf("decided_by = %q, want alice", dec.DecidedBy)
	}
}

// A journal emitter failing on the timeout entry must not break the timeout
// decision itself.
func TestGate_TimeoutEmitFailureStillTimesOut(t *testing.T) {
	db := openGateTestDB(t)
	rec := &hookEmitter{emitErr: fmt.Errorf("journal down")}

	dec, err := Gate(context.Background(), db, rec, NewEvaluatorWithDefaults(), GateInput{
		Mode:         ModeSync,
		Tool:         "deploy_prod",
		WorkspaceID:  "ws_test",
		RequestedBy:  "agent_1",
		PollInterval: time.Hour,
		TimeoutSecs:  1,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.TimedOut || dec.Status != StatusTimeout {
		t.Fatalf("expected TimedOut, got %+v", dec)
	}
	// Row must have been flipped to timeout for audit consistency.
	row, err := Get(context.Background(), db, "ws_test", dec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.Status != StatusTimeout {
		t.Errorf("row status = %+v, want timeout", row)
	}
}

// If the DB dies before the deadline fires, the gate still returns a
// bounded TimedOut decision instead of hanging or erroring.
func TestGate_TimeoutUpdateFailureStillTimesOut(t *testing.T) {
	db := openGateTestDB(t)
	rec := &hookEmitter{hookOn: journal.EntryApprovalRequest, hook: func() {
		time.AfterFunc(100*time.Millisecond, func() { db.Close() })
	}}

	dec, err := Gate(context.Background(), db, rec, NewEvaluatorWithDefaults(), GateInput{
		Mode:         ModeSync,
		Tool:         "deploy_prod",
		WorkspaceID:  "ws_test",
		RequestedBy:  "agent_1",
		PollInterval: time.Hour,
		TimeoutSecs:  1,
	})
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	if !dec.TimedOut {
		t.Fatalf("expected TimedOut despite dead DB, got %+v", dec)
	}
}
