package pipeline

// Monthly budget meter tests (#1422 item 3).

import (
	"context"
	"testing"
	"time"
)

func TestCurrentMonthStart(t *testing.T) {
	got := CurrentMonthStart(time.Date(2026, 7, 24, 15, 30, 0, 0, time.UTC))
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("CurrentMonthStart = %v, want %v", got, want)
	}
}

func TestRunStore_MonthlySpendByPipeline(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	runs := NewRunStore(db)
	now := time.Now().UTC()
	monthStart := CurrentMonthStart(now)

	// In-month spend for two pipelines.
	seedDigestRun(t, db, "run_1", "ws_b", "routine-a", "completed", 5.0, now.Add(-1*time.Hour))
	seedDigestRun(t, db, "run_2", "ws_b", "routine-a", "completed", 2.5, now.Add(-2*time.Hour))
	seedDigestRun(t, db, "run_3", "ws_b", "routine-b", "completed", 1.0, now.Add(-3*time.Hour))
	// Before this month — must not count (only if monthStart isn't day 1..2).
	if now.Day() > 1 {
		seedDigestRun(t, db, "run_old", "ws_b", "routine-a", "completed", 999.0, monthStart.Add(-24*time.Hour))
	}
	// Different workspace — must not leak.
	seedDigestRun(t, db, "run_other", "ws_other", "routine-a", "completed", 50.0, now.Add(-1*time.Hour))

	spend, err := runs.MonthlySpendByPipeline(context.Background(), "ws_b", monthStart)
	if err != nil {
		t.Fatalf("MonthlySpendByPipeline: %v", err)
	}
	if got, want := spend["pln_routine-a"], 7.5; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("spend[routine-a] = %v, want %v", got, want)
	}
	if got, want := spend["pln_routine-b"], 1.0; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("spend[routine-b] = %v, want %v", got, want)
	}
	if _, ok := spend["pln_routine-c"]; ok {
		t.Errorf("unexpected key for a pipeline with no runs")
	}
}

func TestRunStore_MonthlySpendForPipeline(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	runs := NewRunStore(db)
	now := time.Now().UTC()
	monthStart := CurrentMonthStart(now)

	seedDigestRun(t, db, "run_1", "ws_c", "routine-a", "completed", 3.0, now.Add(-1*time.Hour))
	seedDigestRun(t, db, "run_2", "ws_c", "routine-a", "completed", 4.0, now.Add(-2*time.Hour))

	got, err := runs.MonthlySpendForPipeline(context.Background(), "ws_c", "pln_routine-a", monthStart)
	if err != nil {
		t.Fatalf("MonthlySpendForPipeline: %v", err)
	}
	if want := 7.0; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("spend = %v, want %v", got, want)
	}

	// A pipeline with no runs this month reports 0, not an error.
	zero, err := runs.MonthlySpendForPipeline(context.Background(), "ws_c", "pln_nothing", monthStart)
	if err != nil {
		t.Fatalf("MonthlySpendForPipeline (zero case): %v", err)
	}
	if zero != 0 {
		t.Errorf("spend for unknown pipeline = %v, want 0", zero)
	}
}

func TestStore_SetMonthlyBudget(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	store := NewStore(db)
	p, err := store.Save(context.Background(), validSaveInput("budget-target"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if p.MonthlyBudgetUSD != 0 {
		t.Fatalf("default monthly budget = %v, want 0", p.MonthlyBudgetUSD)
	}

	if err := store.SetMonthlyBudget(context.Background(), p.ID, 50); err != nil {
		t.Fatalf("SetMonthlyBudget: %v", err)
	}
	reloaded, err := store.GetByID(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.MonthlyBudgetUSD != 50 {
		t.Errorf("monthly budget = %v, want 50", reloaded.MonthlyBudgetUSD)
	}

	// Clearing back to 0 is allowed (means "no budget set" by convention).
	if err := store.SetMonthlyBudget(context.Background(), p.ID, 0); err != nil {
		t.Fatalf("clear SetMonthlyBudget: %v", err)
	}
	reloaded2, _ := store.GetByID(context.Background(), p.ID)
	if reloaded2.MonthlyBudgetUSD != 0 {
		t.Errorf("cleared monthly budget = %v, want 0", reloaded2.MonthlyBudgetUSD)
	}

	if err := store.SetMonthlyBudget(context.Background(), p.ID, -1); err == nil {
		t.Error("expected error for negative budget")
	}
	if err := store.SetMonthlyBudget(context.Background(), "pln_missing", 10); err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing pipeline, got %v", err)
	}
}
