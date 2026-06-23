package episodic

// Coverage tests for importance.go — every BaseImportance branch,
// RecencyFactor clamping, MarkReferenced reinforcement writes, and the
// DecayAndReinforce timestamp-format fallbacks.

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestBaseImportance_AllTypeSeverityPriorityBranches(t *testing.T) {
	cases := []struct {
		name string
		typ  journal.EntryType
		sev  journal.Severity
		prio journal.Priority
		want float64
	}{
		{"escalation_info", journal.EntryPeerEscalation, journal.SeverityInfo, journal.PriorityNormal, 0.7},
		{"conversation_info", journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityNormal, 0.5},
		{"summary_info", journal.EntrySummaryGenerated, journal.SeverityInfo, journal.PriorityNormal, 0.65},
		{"consolidated_info", journal.EntryMemoryConsolidated, journal.SeverityInfo, journal.PriorityNormal, 0.7},
		{"approval_denied_info", journal.EntryApprovalDenied, journal.SeverityInfo, journal.PriorityNormal, 0.65},
		{"eval_regression_error", journal.EntryEvalRegression, journal.SeverityError, journal.PriorityNormal, 1.0}, // 0.5+0.3+0.25 clamped to 1
		{"keeper_decision_info", journal.EntryKeeperDecision, journal.SeverityInfo, journal.PriorityNormal, 0.6},
		{"mission_status_warn", journal.EntryMissionStatus, journal.SeverityWarn, journal.PriorityNormal, 0.7},
		{"notice_bump", journal.EntryPeerConversation, journal.SeverityNotice, journal.PriorityNormal, 0.55},
		{"permanent_floor", journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityPermanent, 0.95},
		{"high_floor", journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityHigh, 0.85},
		{"pin_floor", journal.EntryPeerConversation, journal.SeverityInfo, journal.PriorityPin, 0.8},
		// Already above the pin floor — floor must not lower the score.
		{"pin_does_not_lower", journal.EntryEvalRegression, journal.SeverityError, journal.PriorityPin, 1.0},
		// Unknown type → neutral base 0.5.
		{"unknown_type", journal.EntryType("something.else"), journal.SeverityInfo, journal.PriorityNormal, 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := float64(BaseImportance(c.typ, c.sev, c.prio))
			if math.Abs(got-c.want) > 1e-9 {
				t.Errorf("BaseImportance(%s, %s, %s) = %v, want %v", c.typ, c.sev, c.prio, got, c.want)
			}
		})
	}
}

func TestRecencyFactor_FutureAndFloor(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Indexed in the future → days clamps to 0 → factor 1.0.
	if got := RecencyFactor(now.Add(48*time.Hour), now); got != 1.0 {
		t.Errorf("future indexedAt should yield 1.0, got %v", got)
	}
	// 90 days old → 1 - 90/180 = 0.5.
	if got := RecencyFactor(now.AddDate(0, 0, -90), now); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("90d old = %v, want 0.5", got)
	}
	// Ancient → floored at 0.1.
	if got := RecencyFactor(now.AddDate(-3, 0, 0), now); got != 0.1 {
		t.Errorf("3y old should floor at 0.1, got %v", got)
	}
}

func TestReferenceBoost_NegativeClampsToZero(t *testing.T) {
	if got := ReferenceBoost(-5); got != 0 {
		t.Errorf("negative refs should clamp to log2(1)=0, got %v", got)
	}
	if got := ReferenceBoost(3); math.Abs(got-2.0) > 1e-9 {
		t.Errorf("ReferenceBoost(3) = %v, want 2 (log2(4))", got)
	}
}

func TestClamp01(t *testing.T) {
	if got := clamp01(-0.3); got != 0 {
		t.Errorf("clamp01(-0.3) = %v, want 0", got)
	}
	if got := clamp01(1.7); got != 1 {
		t.Errorf("clamp01(1.7) = %v, want 1", got)
	}
	if got := clamp01(0.42); got != 0.42 {
		t.Errorf("clamp01(0.42) = %v, want 0.42", got)
	}
}

func TestMarkReferenced_EmptyListIsNoop(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if err := MarkReferenced(context.Background(), db, nil, time.Now()); err != nil {
		t.Fatalf("empty MarkReferenced: %v", err)
	}
}

func TestMarkReferenced_IncrementsAndStamps(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedEmbedding(t, db, "m1", "ws_test", "", "a1", []float32{1, 0, 0, 0}, 4, nil)

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := MarkReferenced(ctx, db, []string{"m1"}, now); err != nil {
		t.Fatalf("MarkReferenced: %v", err)
	}
	if err := MarkReferenced(ctx, db, []string{"m1"}, now.Add(time.Hour)); err != nil {
		t.Fatalf("MarkReferenced 2: %v", err)
	}

	var refs int64
	var lastRef string
	err := db.QueryRow(`SELECT reference_count, last_referenced_at FROM journal_embeddings WHERE entry_id = 'm1'`).
		Scan(&refs, &lastRef)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if refs != 2 {
		t.Errorf("reference_count = %d, want 2", refs)
	}
	wantStamp := now.Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if lastRef != wantStamp {
		t.Errorf("last_referenced_at = %q, want %q", lastRef, wantStamp)
	}
}

func TestDecayAndReinforce_TimestampFormatFallbacks(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	insert := func(id, indexedAt string, refs int64) {
		t.Helper()
		insertEntry(t, db, journal.Entry{
			ID: id, WorkspaceID: "ws_test", AgentID: "a1",
			Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
			ActorType: journal.ActorAgent, Summary: "x",
		})
		if _, err := db.Exec(`INSERT INTO journal_embeddings
			(entry_id, workspace_id, agent_id, model, dim, vector, indexed_at, reference_count)
			VALUES (?, 'ws_test', 'a1', 't', 4, ?, ?, ?)`,
			id, EncodeVector([]float32{1, 0, 0, 0}), indexedAt, refs); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}

	// RFC3339Nano format, 90 days old, no refs → 0.85 * 0.5 = 0.425.
	insert("rfc", now.AddDate(0, 0, -90).Format(time.RFC3339Nano), 0)
	// Legacy "2006-01-02 15:04:05" format, 180 days old → recency floor
	// applies via formula: 1-180/180=0 → max(0.1, 0) = 0.1 → 0.085.
	insert("legacy", now.AddDate(0, 0, -180).Format("2006-01-02 15:04:05"), 0)
	// Garbage timestamp → treated as fresh (recency 1.0). 3 refs →
	// boost log2(4)=2 → 0.85 * 1 * (1 + 2/8) = 1.0625 → clamped 1.0.
	insert("garbage", "not-a-timestamp", 3)

	n, err := DecayAndReinforce(ctx, db, now)
	if err != nil {
		t.Fatalf("DecayAndReinforce: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 rows updated, got %d", n)
	}

	score := func(id string) float64 {
		t.Helper()
		var s float64
		if err := db.QueryRow(`SELECT importance_score FROM journal_embeddings WHERE entry_id = ?`, id).Scan(&s); err != nil {
			t.Fatalf("score %s: %v", id, err)
		}
		return s
	}
	if got := score("rfc"); math.Abs(got-0.425) > 1e-6 {
		t.Errorf("rfc score = %v, want 0.425", got)
	}
	if got := score("legacy"); math.Abs(got-0.085) > 1e-6 {
		t.Errorf("legacy score = %v, want 0.085", got)
	}
	if got := score("garbage"); got != 1.0 {
		t.Errorf("garbage-ts score = %v, want clamped 1.0", got)
	}
}

func TestImportance_ClosedDBErrors(t *testing.T) {
	db := openTestDB(t)
	db.Close()
	ctx := context.Background()

	if _, err := DecayAndReinforce(ctx, db, time.Now()); err == nil {
		t.Error("DecayAndReinforce on closed DB should error")
	}
	if err := MarkReferenced(ctx, db, []string{"x"}, time.Now()); err == nil {
		t.Error("MarkReferenced on closed DB should error")
	}
}

// A RAISE(ABORT) trigger on journal_embeddings updates drives the
// exec-error → rollback branches in MarkReferenced and DecayAndReinforce.
func TestImportance_UpdateFailureRollsBack(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedEmbedding(t, db, "u1", "ws_test", "", "a1", []float32{1, 0, 0, 0}, 4, nil)
	if _, err := db.Exec(`CREATE TRIGGER fail_upd BEFORE UPDATE ON journal_embeddings
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if err := MarkReferenced(ctx, db, []string{"u1"}, time.Now()); err == nil {
		t.Error("MarkReferenced should surface the update failure")
	}
	if _, err := DecayAndReinforce(ctx, db, time.Now()); err == nil {
		t.Error("DecayAndReinforce should surface the update failure")
	}
	var refs int64
	if err := db.QueryRow(`SELECT reference_count FROM journal_embeddings WHERE entry_id='u1'`).Scan(&refs); err != nil {
		t.Fatalf("query: %v", err)
	}
	if refs != 0 {
		t.Errorf("rollback failed: reference_count = %d, want 0", refs)
	}
}

func TestDecayAndReinforce_EmptyTableIsNoop(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	n, err := DecayAndReinforce(context.Background(), db, time.Now())
	if err != nil {
		t.Fatalf("DecayAndReinforce empty: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows, got %d", n)
	}
}
