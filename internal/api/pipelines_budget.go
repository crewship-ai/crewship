package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// budgetResponse is the wire shape for the per-routine budget meter
// (#1422 item 3): a monthly spend cap set out-of-band from the DSL
// (Pipeline.MonthlyBudgetUSD, distinct from DSL.MaxCostUSD's per-run hard
// gate) compared against actual pipeline_runs.cost_usd for the current
// calendar month.
type budgetResponse struct {
	Slug             string  `json:"slug"`
	HasBudget        bool    `json:"has_budget"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	Month            string  `json:"month"` // "2026-07"
	SpentUSD         float64 `json:"spent_usd"`
	// PctUsed and OverBudget are only meaningful when HasBudget is true —
	// omitted (zero value) otherwise so a caller can't misread "0% used"
	// as "budget is $0 and fully unused".
	PctUsed    float64 `json:"pct_used,omitempty"`
	OverBudget bool    `json:"over_budget,omitempty"`
}

func toBudgetResponse(slug string, monthlyBudget, spent float64, monthStart time.Time) budgetResponse {
	resp := budgetResponse{
		Slug:             slug,
		HasBudget:        monthlyBudget > 0,
		MonthlyBudgetUSD: monthlyBudget,
		Month:            monthStart.Format("2006-01"),
		SpentUSD:         spent,
	}
	if resp.HasBudget {
		resp.PctUsed = spent / monthlyBudget * 100
		resp.OverBudget = spent > monthlyBudget
	}
	return resp
}

// GetBudget GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget
func (h *PipelineHandler) GetBudget(w http.ResponseWriter, r *http.Request) {
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run history store not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	monthStart := pipeline.CurrentMonthStart(time.Now())
	spent, err := h.runStore.MonthlySpendForPipeline(r.Context(), workspaceID, p.ID, monthStart)
	if err != nil {
		h.logger.Error("get budget: monthly spend", "error", err, "pipeline_id", p.ID)
		replyError(w, http.StatusInternalServerError, "failed to compute monthly spend")
		return
	}
	writeJSON(w, http.StatusOK, toBudgetResponse(slug, p.MonthlyBudgetUSD, spent, monthStart))
}

// budgetUpdateBody is the PATCH payload for SetBudget.
type budgetUpdateBody struct {
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
}

// SetBudget PATCH /api/v1/workspaces/{workspaceId}/pipelines/{slug}/budget
//
// Sets (or clears, with 0) the routine's monthly spend cap. Independent
// of Save — never touches the DSL, never bumps the version history.
// manage-tier RBAC (see router_pipelines.go): setting a spend cap is an
// operator config change, same tier as pausing/disabling a routine.
func (h *PipelineHandler) SetBudget(w http.ResponseWriter, r *http.Request) {
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run history store not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}

	const maxBody = 1 << 12 // a JSON float field never needs more than a few bytes
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		replyError(w, http.StatusBadRequest, "could not read body")
		return
	}
	var body budgetUpdateBody
	if err := json.Unmarshal(raw, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.MonthlyBudgetUSD < 0 {
		replyError(w, http.StatusBadRequest, "monthly_budget_usd cannot be negative")
		return
	}
	if err := h.store.SetMonthlyBudget(r.Context(), p.ID, body.MonthlyBudgetUSD); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "pipeline not found")
			return
		}
		h.logger.Error("set budget", "error", err, "pipeline_id", p.ID)
		replyError(w, http.StatusInternalServerError, "failed to set monthly budget")
		return
	}

	monthStart := pipeline.CurrentMonthStart(time.Now())
	spent, err := h.runStore.MonthlySpendForPipeline(r.Context(), workspaceID, p.ID, monthStart)
	if err != nil {
		h.logger.Error("set budget: monthly spend", "error", err, "pipeline_id", p.ID)
		replyError(w, http.StatusInternalServerError, "failed to compute monthly spend")
		return
	}
	writeJSON(w, http.StatusOK, toBudgetResponse(slug, body.MonthlyBudgetUSD, spent, monthStart))
}

// budgetSummaryRow is one line of the workspace roll-up.
type budgetSummaryRow struct {
	Slug             string  `json:"slug"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	SpentUSD         float64 `json:"spent_usd"`
	PctUsed          float64 `json:"pct_used,omitempty"`
	OverBudget       bool    `json:"over_budget,omitempty"`
}

// budgetSummaryResponse is the workspace-wide roll-up.
type budgetSummaryResponse struct {
	Month          string             `json:"month"`
	Routines       []budgetSummaryRow `json:"routines"`
	TotalBudgetUSD float64            `json:"total_budget_usd"`
	TotalSpentUSD  float64            `json:"total_spent_usd"`
}

// GetBudgetSummary GET /api/v1/workspaces/{workspaceId}/pipelines/budget-summary
//
// Rolls up every routine that has EITHER a budget set OR spend this
// month — a routine with neither is operationally uninteresting for this
// view and would just be noise (a fresh workspace can have dozens of
// zero-cost deterministic routines).
func (h *PipelineHandler) GetBudgetSummary(w http.ResponseWriter, r *http.Request) {
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run history store not wired")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	pipelines, err := h.store.List(r.Context(), pipeline.ListFilters{WorkspaceID: workspaceID, IncludeHidden: true})
	if err != nil {
		h.logger.Error("budget summary: list pipelines", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to list routines")
		return
	}
	monthStart := pipeline.CurrentMonthStart(time.Now())
	spendByPipeline, err := h.runStore.MonthlySpendByPipeline(r.Context(), workspaceID, monthStart)
	if err != nil {
		h.logger.Error("budget summary: monthly spend", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to compute monthly spend")
		return
	}

	resp := budgetSummaryResponse{Month: monthStart.Format("2006-01"), Routines: []budgetSummaryRow{}}
	for _, p := range pipelines {
		spent := spendByPipeline[p.ID]
		if p.MonthlyBudgetUSD <= 0 && spent <= 0 {
			continue
		}
		row := budgetSummaryRow{Slug: p.Slug, MonthlyBudgetUSD: p.MonthlyBudgetUSD, SpentUSD: spent}
		if p.MonthlyBudgetUSD > 0 {
			row.PctUsed = spent / p.MonthlyBudgetUSD * 100
			row.OverBudget = spent > p.MonthlyBudgetUSD
		}
		resp.Routines = append(resp.Routines, row)
		resp.TotalBudgetUSD += p.MonthlyBudgetUSD
		resp.TotalSpentUSD += spent
	}
	writeJSON(w, http.StatusOK, resp)
}
