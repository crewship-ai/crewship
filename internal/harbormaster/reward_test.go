package harbormaster

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestHashArgsStable(t *testing.T) {
	a := map[string]any{"cmd": "ls", "dir": "/tmp"}
	b := map[string]any{"dir": "/tmp", "cmd": "ls"} // same shape, different insertion
	if HashArgs(a) != HashArgs(b) {
		t.Errorf("HashArgs not stable across key order: %s vs %s", HashArgs(a), HashArgs(b))
	}
	if HashArgs(nil) == "" {
		t.Error("HashArgs(nil) should not return empty (collision with empty-string)")
	}
	if HashArgs(map[string]any{}) != HashArgs(nil) {
		// Empty map and nil both go to "empty" — they're semantically equal
		// from a history-grouping standpoint.
		t.Error("empty and nil should hash to the same bucket")
	}
}

func TestOutcomeCountsRates(t *testing.T) {
	c := OutcomeCounts{Approved: 8, Denied: 2, Timeout: 3, Cancelled: 1, Total: 14}
	if got := c.ApproveRate(); got < 0.79 || got > 0.81 {
		t.Errorf("approve rate: got %.2f want ~0.80", got)
	}
	if got := c.DenyRate(); got < 0.19 || got > 0.21 {
		t.Errorf("deny rate: got %.2f want ~0.20", got)
	}
	empty := OutcomeCounts{}
	if empty.ApproveRate() != 0 || empty.DenyRate() != 0 {
		t.Error("empty counts should give 0/0 rates, not panic")
	}
	// Only timeouts → neither rate should fire.
	toOnly := OutcomeCounts{Timeout: 5, Total: 5}
	if toOnly.ApproveRate() != 0 {
		t.Errorf("timeouts excluded from approve rate: got %.2f", toOnly.ApproveRate())
	}
}

func TestAdjustModeQuorum(t *testing.T) {
	db := openRewardTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ws := "ws_reward"
	tool := "shell.exec"
	args := map[string]any{"cmd": "ls"}

	// 3 approvals — below quorum (10), should NOT tune.
	for i := 0; i < 3; i++ {
		if err := RecordOutcome(ctx, db, ws, tool, args, OutcomeApproved, "user", ""); err != nil {
			t.Fatal(err)
		}
	}
	if adj, _, _ := AdjustMode(ctx, db, ws, tool, args, ModeSync); adj != ModeSync {
		t.Errorf("below quorum should keep sync, got %v", adj)
	}

	// Add 9 more approvals → 12 total, all approved, over 90%.
	for i := 0; i < 9; i++ {
		if err := RecordOutcome(ctx, db, ws, tool, args, OutcomeApproved, "user", ""); err != nil {
			t.Fatal(err)
		}
	}
	adj, reason, _ := AdjustMode(ctx, db, ws, tool, args, ModeSync)
	if adj != ModeAsync {
		t.Errorf("12 approvals → want async, got %v (%s)", adj, reason)
	}
	if reason == "" {
		t.Error("auto-downgrade should produce a non-empty reason")
	}

	// ModeNone should NEVER be adjusted regardless of history.
	if adj, _, _ := AdjustMode(ctx, db, ws, tool, args, ModeNone); adj != ModeNone {
		t.Errorf("ModeNone must not be tuned, got %v", adj)
	}
}

func TestAdjustModeUpgrade(t *testing.T) {
	db := openRewardTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ws := "ws_reward"
	tool := "rm"
	args := map[string]any{"target": "/prod"}

	// 12 denials over 70%.
	for i := 0; i < 12; i++ {
		if err := RecordOutcome(ctx, db, ws, tool, args, OutcomeDenied, "user", ""); err != nil {
			t.Fatal(err)
		}
	}
	adj, reason, _ := AdjustMode(ctx, db, ws, tool, args, ModeAsync)
	if adj != ModeSync {
		t.Errorf("12 denials → want sync upgrade, got %v (%s)", adj, reason)
	}
}

// Regression: quorum must count only approved+denied — timeouts and
// cancellations are non-signal. Before the fix, 9 timeouts + 1 approve
// passed quorum (Total=10 >= 10) and flipped sync→async after a single
// real human decision, which is the exact opposite of the safety margin.
func TestAdjustModeQuorumIgnoresTimeouts(t *testing.T) {
	db := openRewardTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ws := "ws_reward"
	tool := "shell.exec"
	args := map[string]any{"cmd": "ls"}

	// 9 timeouts — non-signal outcomes that must NOT count toward quorum.
	for i := 0; i < 9; i++ {
		if err := RecordOutcome(ctx, db, ws, tool, args, OutcomeTimeout, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	// Plus 1 real approval → Total=10, decided=1. Quorum must fail.
	if err := RecordOutcome(ctx, db, ws, tool, args, OutcomeApproved, "user", ""); err != nil {
		t.Fatal(err)
	}
	adj, reason, _ := AdjustMode(ctx, db, ws, tool, args, ModeSync)
	if adj != ModeSync {
		t.Errorf("1 real decision + 9 timeouts must NOT tune, got %v (%s)", adj, reason)
	}
}

func TestResetAutoTuning(t *testing.T) {
	db := openRewardTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ws := "ws_reward"
	tool := "shell.exec"
	args := map[string]any{"cmd": "ls"}

	for i := 0; i < 5; i++ {
		RecordOutcome(ctx, db, ws, tool, args, OutcomeApproved, "user", "")
	}
	n, err := ResetAutoTuning(ctx, db, ws, tool)
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("reset: deleted %d want 5", n)
	}
	// Different workspace, same tool — should NOT be affected.
	for i := 0; i < 3; i++ {
		RecordOutcome(ctx, db, "ws_other", tool, args, OutcomeApproved, "u", "")
	}
	n, _ = ResetAutoTuning(ctx, db, ws, tool)
	if n != 0 {
		t.Errorf("ws_reward already cleared, got %d extras", n)
	}
	counts, _ := RewardHistory(ctx, db, "ws_other", tool, HashArgs(args), 20)
	if counts.Total != 3 {
		t.Errorf("cross-tenant leak: ws_other should still have 3, got %d", counts.Total)
	}
}

func openRewardTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		CREATE TABLE gate_reward_history (
			id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
			tool_name TEXT NOT NULL, args_hash TEXT NOT NULL,
			outcome TEXT NOT NULL, decided_by TEXT,
			decided_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			request_id TEXT);`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
