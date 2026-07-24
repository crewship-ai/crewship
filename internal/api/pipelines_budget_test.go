package api

// Per-routine monthly budget meter API tests (#1422 item 3).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestGetBudget_NoBudgetSet(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineWithVersions(t, h, wsID, "pln-b1", "budget-none", 1)

	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("slug", "budget-none")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetBudget(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp budgetResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.HasBudget {
		t.Errorf("has_budget = true, want false")
	}
	if resp.SpentUSD != 0 {
		t.Errorf("spent_usd = %v, want 0", resp.SpentUSD)
	}
	if resp.Month == "" {
		t.Error("month should be populated")
	}
}

func TestSetBudget_And_GetBudget_RoundTrip(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineWithVersions(t, h, wsID, "pln-b2", "budget-set", 1)

	patchReq := withWorkspaceUser(httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"monthly_budget_usd": 25.5}`)),
		userID, wsID, "OWNER")
	patchReq.SetPathValue("slug", "budget-set")
	rr := httptest.NewRecorder()
	h.SetBudget(rr, patchReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var patchResp budgetResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &patchResp); err != nil {
		t.Fatalf("decode patch resp: %v", err)
	}
	if !patchResp.HasBudget || patchResp.MonthlyBudgetUSD != 25.5 {
		t.Errorf("patch resp = %+v, want has_budget=true monthly_budget_usd=25.5", patchResp)
	}

	// Seed some in-month spend directly, then re-GET.
	now := time.Now().UTC()
	if _, err := h.db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, cost_usd, created_at, updated_at)
		VALUES ('run_b1', ?, 'pln-b2', 'budget-set', 'completed', ?, 10.0, ?, ?)`,
		wsID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	getReq := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	getReq.SetPathValue("slug", "budget-set")
	rr2 := httptest.NewRecorder()
	h.GetBudget(rr2, getReq)
	if rr2.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rr2.Code, rr2.Body.String())
	}
	var getResp budgetResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode get resp: %v", err)
	}
	if getResp.SpentUSD != 10.0 {
		t.Errorf("spent_usd = %v, want 10.0", getResp.SpentUSD)
	}
	wantPct := 10.0 / 25.5 * 100
	if getResp.PctUsed < wantPct-0.01 || getResp.PctUsed > wantPct+0.01 {
		t.Errorf("pct_used = %v, want ~%v", getResp.PctUsed, wantPct)
	}
	if getResp.OverBudget {
		t.Error("over_budget should be false at 10/25.5")
	}
}

func TestSetBudget_NegativeRejected(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineWithVersions(t, h, wsID, "pln-b3", "budget-neg", 1)

	req := withWorkspaceUser(httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"monthly_budget_usd": -5}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("slug", "budget-neg")
	rr := httptest.NewRecorder()
	h.SetBudget(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestGetBudget_UnknownRoutine_404(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))

	req := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	req.SetPathValue("slug", "ghost")
	rr := httptest.NewRecorder()
	h.GetBudget(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestGetBudget_NoRunStore_503(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-b4", "budget-no-store", 1)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	req.SetPathValue("slug", "budget-no-store")
	rr := httptest.NewRecorder()
	h.GetBudget(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestGetBudgetSummary_ExcludesZeroZero(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	h.SetRunStore(pipeline.NewRunStore(h.db))
	seedPipelineWithVersions(t, h, wsID, "pln-s1", "summary-budgeted", 1)
	seedPipelineWithVersions(t, h, wsID, "pln-s2", "summary-spent-nobudget", 1)
	seedPipelineWithVersions(t, h, wsID, "pln-s3", "summary-untouched", 1)

	patchReq := withWorkspaceUser(httptest.NewRequest("PATCH", "/x", strings.NewReader(`{"monthly_budget_usd": 100}`)),
		userID, wsID, "OWNER")
	patchReq.SetPathValue("slug", "summary-budgeted")
	h.SetBudget(httptest.NewRecorder(), patchReq)

	now := time.Now().UTC()
	if _, err := h.db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, cost_usd, created_at, updated_at)
		VALUES ('run_s1', ?, 'pln-s2', 'summary-spent-nobudget', 'completed', ?, 3.0, ?, ?)`,
		wsID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("GET", "/x", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.GetBudgetSummary(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp budgetSummaryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	slugs := map[string]budgetSummaryRow{}
	for _, r := range resp.Routines {
		slugs[r.Slug] = r
	}
	if _, ok := slugs["summary-budgeted"]; !ok {
		t.Error("summary-budgeted (has budget) missing from rollup")
	}
	if _, ok := slugs["summary-spent-nobudget"]; !ok {
		t.Error("summary-spent-nobudget (has spend) missing from rollup")
	}
	if _, ok := slugs["summary-untouched"]; ok {
		t.Error("summary-untouched (no budget, no spend) should be excluded")
	}
	if resp.TotalBudgetUSD != 100 {
		t.Errorf("total_budget_usd = %v, want 100", resp.TotalBudgetUSD)
	}
	if resp.TotalSpentUSD != 3.0 {
		t.Errorf("total_spent_usd = %v, want 3.0", resp.TotalSpentUSD)
	}
}
