package main

// #1422 item 3: `routine budget get/set/summary` CLI parity for the
// per-routine monthly budget meter API.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestRoutineBudgetGet(t *testing.T) {
	budgetPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/my-routine/budget"

	t.Run("happy path", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet(budgetPath, clitest.JSONResponse(200, budgetRow{
			Slug: "my-routine", HasBudget: true, MonthlyBudgetUSD: usdPtr(50), Month: "2026-07",
			SpentUSD: usdPtr(12.5), PctUsed: usdPtr(25), OverBudget: false,
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
			Slug: "my-routine", HasBudget: false, Month: "2026-07", SpentUSD: usdPtr(3.0),
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

	// #3590: below ADMIN the server keeps has_budget / over_budget and
	// withholds the amounts. The CLI must say so, not print "$0.00".
	t.Run("amounts withheld below ADMIN", func(t *testing.T) {
		for _, c := range []struct {
			name string
			row  string
			want string
		}{
			{"over", `{"slug":"my-routine","has_budget":true,"month":"2026-07","over_budget":true}`, "OVER BUDGET"},
			{"within", `{"slug":"my-routine","has_budget":true,"month":"2026-07"}`, "within budget"},
			{"none", `{"slug":"my-routine","has_budget":false,"month":"2026-07"}`, "no budget set"},
		} {
			t.Run(c.name, func(t *testing.T) {
				stub := covStub(t)
				stub.OnGet(budgetPath, clitest.JSONResponse(200, json.RawMessage(c.row)))
				covResetFlags(t, routineBudgetGetCmd)
				out := covCaptureStdoutCli3(t, func() {
					if err := routineBudgetGetCmd.RunE(routineBudgetGetCmd, []string{"my-routine"}); err != nil {
						t.Errorf("RunE: %v", err)
					}
				})
				if !strings.Contains(out, c.want) || !strings.Contains(out, "OWNER and ADMIN") {
					t.Errorf("want %q and the OWNER/ADMIN note:\n%s", c.want, out)
				}
				if strings.Contains(out, "$") {
					t.Errorf("withheld amounts printed as dollars:\n%s", out)
				}
			})
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
			Slug: "my-routine", HasBudget: true, MonthlyBudgetUSD: usdPtr(75), Month: "2026-07",
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
			{Slug: "a", HasBudget: true, MonthlyBudgetUSD: usdPtr(100), SpentUSD: usdPtr(40), PctUsed: usdPtr(40)},
			{Slug: "b", HasBudget: false, SpentUSD: usdPtr(5)},
		},
		TotalBudgetUSD: usdPtr(100),
		TotalSpentUSD:  usdPtr(45),
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

// #3590: the summary below ADMIN carries rows without amounts.
func TestRoutineBudgetSummary_AmountsWithheld(t *testing.T) {
	summaryPath := "/api/v1/workspaces/" + covWSCli3 + "/pipelines/budget-summary"
	stub := covStub(t)
	stub.OnGet(summaryPath, clitest.JSONResponse(200, json.RawMessage(
		`{"month":"2026-07","routines":[{"slug":"a","has_budget":true,"over_budget":true},{"slug":"b","has_budget":false}]}`)))
	covResetFlags(t, routineBudgetSummaryCmd)
	out := covCaptureStdoutCli3(t, func() {
		if err := routineBudgetSummaryCmd.RunE(routineBudgetSummaryCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "OVER") || !strings.Contains(out, "OWNER and ADMIN") {
		t.Errorf("want OVER state and the OWNER/ADMIN note:\n%s", out)
	}
	if strings.Contains(out, "$") {
		t.Errorf("withheld amounts printed as dollars:\n%s", out)
	}
}

func usdPtr(v float64) *float64 { return &v }
