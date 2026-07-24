package main

// #1422 item 3: `routine budget get/set/summary` CLI parity for the
// per-routine monthly budget meter API.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineBudgetGet(t *testing.T) {
	budgetPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/my-routine/budget"

	t.Run("happy path", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(budgetPath, clitest.JSONResponse(200, budgetRow{
			Slug: "my-routine", HasBudget: true, MonthlyBudgetUSD: 50, Month: "2026-07",
			SpentUSD: 12.5, PctUsed: 25, OverBudget: false,
		}))
		covResetFlags(t, routineBudgetGetCmd)
		out := covCaptureStdoutCli3(t, func() {
			if err := routineBudgetGetCmd.RunE(routineBudgetGetCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "$12.50") || !strings.Contains(out, "$50.00") || !strings.Contains(out, "25") {
			t.Errorf("output missing spend/budget/pct:\n%s", out)
		}
	})

	t.Run("no budget set", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(budgetPath, clitest.JSONResponse(200, budgetRow{
			Slug: "my-routine", HasBudget: false, Month: "2026-07", SpentUSD: 3.0,
		}))
		covResetFlags(t, routineBudgetGetCmd)
		out := covCaptureStdoutCli3(t, func() {
			if err := routineBudgetGetCmd.RunE(routineBudgetGetCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "no budget set") {
			t.Errorf("expected no-budget message:\n%s", out)
		}
	})

	t.Run("api error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(budgetPath, clitest.ErrorResponse(404, "pipeline not found"))
		covResetFlags(t, routineBudgetGetCmd)
		err := routineBudgetGetCmd.RunE(routineBudgetGetCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "pipeline not found") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRoutineBudgetSet(t *testing.T) {
	budgetPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/my-routine/budget"

	t.Run("requires --amount", func(t *testing.T) {
		covStub(t)
		covResetFlags(t, routineBudgetSetCmd)
		err := routineBudgetSetCmd.RunE(routineBudgetSetCmd, []string{"my-routine"})
		if err == nil || !strings.Contains(err.Error(), "--amount") {
			t.Fatalf("expected --amount required error, got %v", err)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPatch(budgetPath, clitest.JSONResponse(200, budgetRow{
			Slug: "my-routine", HasBudget: true, MonthlyBudgetUSD: 75, Month: "2026-07",
		}))
		covResetFlags(t, routineBudgetSetCmd)
		covSetFlags(t, routineBudgetSetCmd, map[string]string{"amount": "75"})
		out := covCaptureStdoutCli3(t, func() {
			if err := routineBudgetSetCmd.RunE(routineBudgetSetCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "$75.00") {
			t.Errorf("missing confirmation:\n%s", out)
		}
		calls := stub.CallsFor("PATCH", budgetPath)
		if len(calls) != 1 {
			t.Fatalf("PATCH calls = %d", len(calls))
		}
		var body map[string]any
		clitest.MustDecodeJSONBody(calls[0].Body, &body)
		if body["monthly_budget_usd"] != float64(75) {
			t.Errorf("monthly_budget_usd = %v, want 75", body["monthly_budget_usd"])
		}
	})

	t.Run("zero clears the budget", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPatch(budgetPath, clitest.JSONResponse(200, budgetRow{
			Slug: "my-routine", HasBudget: false, Month: "2026-07",
		}))
		covResetFlags(t, routineBudgetSetCmd)
		covSetFlags(t, routineBudgetSetCmd, map[string]string{"amount": "0"})
		out := covCaptureStdoutCli3(t, func() {
			if err := routineBudgetSetCmd.RunE(routineBudgetSetCmd, []string{"my-routine"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "cleared") {
			t.Errorf("expected cleared message:\n%s", out)
		}
	})
}

func TestRoutineBudgetSummary(t *testing.T) {
	summaryPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/budget-summary"

	stub := covStub(t)
	stub.OnGet(summaryPath, clitest.JSONResponse(200, budgetSummaryRowsResponse{
		Month: "2026-07",
		Routines: []budgetRow{
			{Slug: "a", HasBudget: true, MonthlyBudgetUSD: 100, SpentUSD: 40, PctUsed: 40},
			{Slug: "b", HasBudget: false, SpentUSD: 5},
		},
		TotalBudgetUSD: 100,
		TotalSpentUSD:  45,
	}))
	covResetFlags(t, routineBudgetSummaryCmd)
	out := covCaptureStdoutCli3(t, func() {
		if err := routineBudgetSummaryCmd.RunE(routineBudgetSummaryCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Errorf("missing routine rows:\n%s", out)
	}
	if !strings.Contains(out, "$45.00") {
		t.Errorf("missing total spend:\n%s", out)
	}
}

func TestRoutineBudgetSummary_Empty(t *testing.T) {
	summaryPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/budget-summary"
	stub := covStub(t)
	stub.OnGet(summaryPath, clitest.JSONResponse(200, budgetSummaryRowsResponse{
		Month: "2026-07", Routines: []budgetRow{},
	}))
	covResetFlags(t, routineBudgetSummaryCmd)
	out := covCaptureStdoutCli3(t, func() {
		if err := routineBudgetSummaryCmd.RunE(routineBudgetSummaryCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "No routines") {
		t.Errorf("expected empty-state message:\n%s", out)
	}
}
