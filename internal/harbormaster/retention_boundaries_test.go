package harbormaster

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRetentionPreviewMatchesDeletionAndLeavesPendingApprovals(t *testing.T) {
	db := openTestDB(t)
	seedApprovalRows(t, db, []seedRow{
		{id: "terminal", workspaceID: "ws_test", status: "approved", decidedAgeDays: 120},
		{id: "pending", workspaceID: "ws_test", status: "pending"},
		{id: "fresh", workspaceID: "ws_test", status: "denied", decidedAgeDays: 2},
	})
	for _, tc := range []struct {
		ws   string
		days int
		want int64
		fail bool
	}{
		{"ws_test", 90, 1, false}, {"ws_test", 0, 0, false}, {"", 90, 0, false}, {"ws_test", MaxApprovalsRetentionDays + 1, 0, true}, {"other", 90, 0, false},
	} {
		n, err := CountApprovalsRetention(t.Context(), db, tc.ws, tc.days, time.Now())
		if n != tc.want || (err != nil) != tc.fail {
			t.Fatalf("preview(%s,%d)=%d %v", tc.ws, tc.days, n, err)
		}
	}
	if len(approvalRowIDs(t, db, "ws_test")) != 3 {
		t.Fatal("preview deleted rows")
	}
	n, capped, err := SweepApprovalsRetention(t.Context(), db, "ws_test", 90)
	if err != nil || n != 1 || capped {
		t.Fatalf("sweep=%d capped=%v %v", n, capped, err)
	}
	remaining := approvalRowIDs(t, db, "ws_test")
	if !remaining["pending"] || !remaining["fresh"] || remaining["terminal"] {
		t.Fatalf("survivors=%v", remaining)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := CountApprovalsRetention(t.Context(), db, "ws_test", 90, time.Now()); err == nil {
		t.Fatal("unavailable preview became zero")
	}
	if err := SweepAllWorkspacesApprovalsRetention(t.Context(), db, nil); err == nil {
		t.Fatal("unavailable sweep became success")
	}
}

type approvalSweepSignal struct {
	slog.Handler
	messages chan string
}

func (h approvalSweepSignal) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.messages <- r.Message:
	default:
	}
	return nil
}
func TestApprovalRetentionSweeperRunsImmediatelyAndStopsAfterPeriodicFailure(t *testing.T) {
	db := openTestDB(t)
	seedApprovalRows(t, db, []seedRow{{id: "old", workspaceID: "ws_test", status: "approved", decidedAgeDays: 120}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	messages := make(chan string, 16)
	logger := slog.New(approvalSweepSignal{Handler: slog.Default().Handler(), messages: messages})
	done := make(chan struct{})
	go func() { defer close(done); StartApprovalsRetentionSweeper(ctx, db, logger, 5*time.Millisecond) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("sweeper did not terminate")
		}
	})
	select {
	case message := <-messages:
		if message != "approvals retention sweep" {
			t.Fatalf("initial sweep log=%s", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial sweep never ran")
	}
	if len(approvalRowIDs(t, db, "ws_test")) != 0 {
		t.Fatal("initial sweep did not remove expired approval")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-messages:
		if message != "approvals retention sweeper: tick failed" {
			t.Fatalf("outage log=%s", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("periodic outage hidden")
	}
	cancel()
	// Defaults and an initial error still honor an already canceled lifecycle.
	StartApprovalsRetentionSweeper(ctx, nil, nil, 0)
}

func TestApprovalRulesPreserveThresholdsAcrossInputRepresentations(t *testing.T) {
	evaluator := NewEvaluator(RuleMatcher{CostThresholdUSD: 10})
	for _, value := range []any{float64(10), float32(10), int(10), int32(10), int64(10), uint(10), uint64(10), "10.5"} {
		t.Run(fmt.Sprintf("%T", value), func(t *testing.T) {
			required, reason, kind := evaluator.Evaluate(t.Context(), "charge", map[string]any{"cost_estimate_usd": value})
			if !required || kind != KindCustom || !strings.Contains(reason, "cost_estimate_usd=") {
				t.Fatalf("threshold lost for %T: %v %s %s", value, required, reason, kind)
			}
		})
	}
	for _, value := range []any{nil, true, []int{10}, "unknown", 9.9} {
		if required, _, _ := evaluator.Evaluate(t.Context(), "charge", map[string]any{"cost_estimate_usd": value}); required {
			t.Fatalf("nonmatching input requires approval: %#v", value)
		}
	}
	env := NewEvaluator(RuleMatcher{TargetEnvPatterns: []string{"", "production"}})
	if required, _, _ := env.Evaluate(t.Context(), "deploy", map[string]any{"target": 12, "environment": "PRODUCTION-west"}); !required {
		t.Fatal("environment match ignored after malformed target")
	}
	predicate := NewEvaluator(RuleMatcher{RequireWhen: func(tool string, args map[string]any) bool { return tool == "erase" }})
	if required, _, kind := predicate.Evaluate(t.Context(), "erase", nil); !required || kind != KindCustom {
		t.Fatal("unnamed custom rule ignored")
	}
}

func TestRewardStorageFailureCannotMasqueradeAsAnEmptyHistory(t *testing.T) {
	db := openRewardTestDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RecordOutcome(t.Context(), db, "", "tool", nil, OutcomeApproved, "", ""); err == nil {
		t.Fatal("empty workspace recorded")
	}
	if err := RecordOutcomeAt(t.Context(), db, "ws", "", nil, OutcomeApproved, "", "", time.Now()); err == nil {
		t.Fatal("empty tool recorded")
	}
	if err := RecordOutcome(t.Context(), db, "ws", "tool", nil, OutcomeApproved, "", ""); err == nil {
		t.Fatal("failed outcome insert reported success")
	}
	if _, err := RewardHistory(t.Context(), db, "ws", "tool", "empty", 0); err == nil {
		t.Fatal("failed history reported empty")
	}
	if _, err := ResetAutoTuning(t.Context(), db, "ws", "tool"); err == nil {
		t.Fatal("failed history reset reported success")
	}
}
