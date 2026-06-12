package paymaster

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// insertLedgerRow writes one cost_ledger row with explicit nullable
// crew/agent/mission columns so the COALESCE branches can be exercised.
func insertLedgerRow(t *testing.T, db *sql.DB, id, ws string, crew, agent, mission any,
	ts time.Time, cost float64, in, out int64, billingMode, plan string) {
	t.Helper()
	if billingMode == "" {
		billingMode = "metered"
	}
	_, err := db.ExecContext(context.Background(), `INSERT INTO cost_ledger
		(id, workspace_id, crew_id, agent_id, mission_id, ts, provider, model,
		 input_tokens, output_tokens, cost_usd, billing_mode, subscription_plan)
		VALUES (?, ?, ?, ?, ?, ?, 'anthropic', 'claude-haiku-4-5', ?, ?, ?, ?, ?)`,
		id, ws, crew, agent, mission, ts.UTC().Format(tsLayout), in, out, cost, billingMode,
		sql.NullString{String: plan, Valid: plan != ""})
	if err != nil {
		t.Fatalf("insert ledger row %s: %v", id, err)
	}
}

func TestRollups_RequireScopeIDs(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := SpendByCrew(ctx, db, "", time.Time{}, time.Time{}); err == nil {
		t.Error("SpendByCrew accepted empty workspace_id")
	}
	if _, err := SpendByAgent(ctx, db, "", time.Time{}, time.Time{}); err == nil {
		t.Error("SpendByAgent accepted empty crew_id")
	}
	if _, err := SpendByMission(ctx, db, ""); err == nil {
		t.Error("SpendByMission accepted empty mission_id")
	}
	if _, err := SubscriptionUsageByPlan(ctx, db, "", time.Time{}, time.Time{}); err == nil {
		t.Error("SubscriptionUsageByPlan accepted empty workspace_id")
	}
	if _, err := TopSpenders(ctx, db, "", 5, time.Time{}); err == nil {
		t.Error("TopSpenders accepted empty workspace_id")
	}
}

func TestSpendByCrew_WindowBoundsAndNullCrew(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	insertLedgerRow(t, db, "r1", "ws1", "crewA", "ag1", nil, base.Add(-48*time.Hour), 5.0, 100, 50, "", "")
	insertLedgerRow(t, db, "r2", "ws1", "crewA", "ag1", nil, base, 2.0, 10, 5, "", "")
	insertLedgerRow(t, db, "r3", "ws1", nil, "ag2", nil, base, 1.0, 7, 3, "", "") // unattributed
	insertLedgerRow(t, db, "r4", "ws1", "crewB", "ag3", nil, base.Add(48*time.Hour), 9.0, 1, 1, "", "")

	// Window [base-1h, base+1h) keeps only r2 and r3.
	got, err := SpendByCrew(ctx, db, "ws1", base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SpendByCrew: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (window must exclude r1/r4): %+v", len(got), got)
	}
	// Ordered by cost DESC: crewA ($2) then unattributed ($1, CrewID "").
	if got[0].CrewID != "crewA" || got[0].CostUSD != 2.0 || got[0].CallCount != 1 {
		t.Errorf("row0 = %+v, want crewA $2 x1", got[0])
	}
	if got[1].CrewID != "" || got[1].CostUSD != 1.0 || got[1].InTokens != 7 {
		t.Errorf("row1 = %+v, want unattributed $1 in=7", got[1])
	}
}

func TestSpendByAgent_NullAgentSurfacesEmpty(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	insertLedgerRow(t, db, "a1", "ws1", "crewA", "agent1", nil, base, 3.0, 30, 15, "", "")
	insertLedgerRow(t, db, "a2", "ws1", "crewA", nil, nil, base, 1.5, 10, 5, "", "")

	got, err := SpendByAgent(ctx, db, "crewA", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("SpendByAgent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(got), got)
	}
	if got[0].AgentID != "agent1" || got[0].CostUSD != 3.0 {
		t.Errorf("row0 = %+v, want agent1 $3", got[0])
	}
	if got[1].AgentID != "" || got[1].CostUSD != 1.5 {
		t.Errorf("row1 = %+v, want unattributed $1.5", got[1])
	}
}

func TestSpendByMission_RowsAndTimestamps(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	first := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	last := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)

	insertLedgerRow(t, db, "m1", "ws1", "crewA", "ag1", "missionX", first, 1.0, 10, 5, "", "")
	insertLedgerRow(t, db, "m2", "ws1", "crewB", "ag2", "missionX", last, 2.0, 20, 10, "", "")

	got, err := SpendByMission(ctx, db, "missionX")
	if err != nil {
		t.Fatalf("SpendByMission: %v", err)
	}
	if got.MissionID != "missionX" || got.CostUSD != 3.0 || got.CallCount != 2 {
		t.Errorf("rollup = %+v, want missionX $3 x2", got)
	}
	if got.InTokens != 30 || got.OutTokens != 15 {
		t.Errorf("tokens = %d/%d, want 30/15", got.InTokens, got.OutTokens)
	}
	if !got.FirstTS.Equal(first) || !got.LastTS.Equal(last) {
		t.Errorf("window = %v..%v, want %v..%v", got.FirstTS, got.LastTS, first, last)
	}
}

func TestSpendByMission_NoSpendIsZeroValue(t *testing.T) {
	db := openTestDB(t)
	got, err := SpendByMission(context.Background(), db, "ghost-mission")
	if err != nil {
		t.Fatalf("SpendByMission: %v", err)
	}
	if got.CostUSD != 0 || got.CallCount != 0 {
		t.Errorf("rollup = %+v, want zeros", got)
	}
	if !got.FirstTS.IsZero() || !got.LastTS.IsZero() {
		t.Errorf("timestamps = %v/%v, want zero times", got.FirstTS, got.LastTS)
	}
}

func TestSubscriptionUsageByPlan_WindowAndUnknownPlan(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	insertLedgerRow(t, db, "s1", "ws1", "crewA", "ag1", nil, base, 0, 100, 50, "flat_rate", "claude-max")
	insertLedgerRow(t, db, "s2", "ws1", "crewA", "ag1", nil, base.Add(time.Minute), 0, 200, 100, "flat_rate", "claude-max")
	insertLedgerRow(t, db, "s3", "ws1", "crewA", "ag2", nil, base, 0, 10, 5, "flat_rate", "") // empty plan → "unknown"
	insertLedgerRow(t, db, "s4", "ws1", "crewA", "ag1", nil, base, 4.0, 40, 20, "metered", "")
	insertLedgerRow(t, db, "s5", "ws1", "crewA", "ag1", nil, base.Add(72*time.Hour), 0, 1, 1, "flat_rate", "claude-max")

	got, err := SubscriptionUsageByPlan(ctx, db, "ws1", base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("SubscriptionUsageByPlan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (metered + out-of-window excluded): %+v", len(got), got)
	}
	// Ordered by call count DESC: claude-max (2 calls) then unknown (1).
	if got[0].SubscriptionPlan != "claude-max" || got[0].CallCount != 2 ||
		got[0].InTokens != 300 || got[0].Provider != "anthropic" {
		t.Errorf("row0 = %+v, want claude-max x2 in=300", got[0])
	}
	if !got[0].LastTS.Equal(base.Add(time.Minute)) {
		t.Errorf("LastTS = %v, want %v", got[0].LastTS, base.Add(time.Minute))
	}
	if got[1].SubscriptionPlan != "unknown" || got[1].CallCount != 1 {
		t.Errorf("row1 = %+v, want unknown x1", got[1])
	}
}

func TestTopSpenders_ClampsLimitAndFiltersSince(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	insertLedgerRow(t, db, "t1", "ws1", "crewA", "agent-big", nil, base, 8.0, 1, 1, "", "")
	insertLedgerRow(t, db, "t2", "ws1", "crewA", "agent-small", nil, base, 2.0, 1, 1, "", "")
	insertLedgerRow(t, db, "t3", "ws1", "crewA", nil, nil, base, 99.0, 1, 1, "", "")                            // NULL agent excluded
	insertLedgerRow(t, db, "t4", "ws1", "crewA", "agent-old", nil, base.Add(-72*time.Hour), 50.0, 1, 1, "", "") // pre-window

	// limit <= 0 falls back to 10; since filters out agent-old.
	got, err := TopSpenders(ctx, db, "ws1", 0, base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("TopSpenders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(got), got)
	}
	if got[0].ID != "agent-big" || got[0].CostUSD != 8.0 || got[0].Kind != "agent" {
		t.Errorf("row0 = %+v, want agent-big $8", got[0])
	}
	if got[1].ID != "agent-small" {
		t.Errorf("row1 = %+v, want agent-small", got[1])
	}

	// Oversized limit is clamped, not an error.
	got2, err := TopSpenders(ctx, db, "ws1", 5000, time.Time{})
	if err != nil {
		t.Fatalf("TopSpenders clamped: %v", err)
	}
	if len(got2) != 3 { // agent-big, agent-old, agent-small over all time
		t.Errorf("rows = %d, want 3: %+v", len(got2), got2)
	}
}

func TestBuildAggregateQuery_AllFilters(t *testing.T) {
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	q, args := buildAggregateQuery("SELECT 1 FROM cost_ledger", "GROUP BY x",
		"ws1", "crew1", "agent1", "mission1", since, until)

	for _, want := range []string{
		"workspace_id = ?", "crew_id = ?", "agent_id = ?", "mission_id = ?",
		"ts >= ?", "ts < ?", "WHERE", "GROUP BY x",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q: %s", want, q)
		}
	}
	if len(args) != 6 {
		t.Fatalf("args = %d, want 6: %v", len(args), args)
	}
	wantArgs := []any{"ws1", "crew1", "agent1", "mission1",
		since.Format(tsLayout), until.Format(tsLayout)}
	for i := range wantArgs {
		if fmt.Sprint(args[i]) != fmt.Sprint(wantArgs[i]) {
			t.Errorf("args[%d] = %v, want %v", i, args[i], wantArgs[i])
		}
	}
}

func TestBuildAggregateQuery_NoFiltersOmitsWhere(t *testing.T) {
	q, args := buildAggregateQuery("SELECT 1 FROM cost_ledger", "GROUP BY x",
		"", "", "", "", time.Time{}, time.Time{})
	if strings.Contains(q, "WHERE") {
		t.Errorf("query has WHERE with no conditions: %s", q)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}
}

func TestRollups_QueryErrorsOnClosedDB(t *testing.T) {
	db := openTestDB(t)
	_ = db.Close()
	ctx := context.Background()

	if _, err := SpendByCrew(ctx, db, "ws1", time.Time{}, time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "query crew spend") {
		t.Errorf("SpendByCrew closed-db err = %v", err)
	}
	if _, err := SpendByAgent(ctx, db, "crew1", time.Time{}, time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "query agent spend") {
		t.Errorf("SpendByAgent closed-db err = %v", err)
	}
	if _, err := SpendByMission(ctx, db, "m1"); err == nil ||
		!strings.Contains(err.Error(), "query mission spend") {
		t.Errorf("SpendByMission closed-db err = %v", err)
	}
	if _, err := SubscriptionUsageByPlan(ctx, db, "ws1", time.Time{}, time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "query subscription usage") {
		t.Errorf("SubscriptionUsageByPlan closed-db err = %v", err)
	}
	if _, err := TopSpenders(ctx, db, "ws1", 5, time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "query top spenders") {
		t.Errorf("TopSpenders closed-db err = %v", err)
	}
}
