package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// routineHealthRow is one routine of the Health table (ODCH-RT-07).
type routineHealthRow struct {
	Slug string `json:"slug"`
	// LastOutcomes are the newest runs, oldest first (at most 20): completed,
	// failed, stopped, waiting, running.
	LastOutcomes []string `json:"last_outcomes"`
	Finished     int      `json:"finished"`
	Succeeded    int      `json:"succeeded"`
	// SuccessPct is over the finished runs among LastOutcomes; absent when
	// none finished (no figure rather than 0 %).
	SuccessPct *int    `json:"success_pct,omitempty"`
	Runs       int     `json:"runs"`
	SpendUSD   float64 `json:"spend_usd"`
}

type routineHealthResponse struct {
	WindowDays int                `json:"window_days"`
	Routines   []routineHealthRow `json:"routines"`
}

const (
	healthDefaultDays = 30
	healthMaxDays     = 366
	healthLastRuns    = 20
)

// GetHealth GET /api/v1/workspaces/{workspaceId}/pipeline-health?days=30
//
// Per routine with at least one run: its last 20 outcomes, the success rate
// over the finished ones, and the runs and spend of the last `days` days
// (default 30, up to 366). Test and dry runs are not counted. The monthly
// meter of /pipelines/budget-summary is a calendar month; this is a rolling
// window, which is what the Routines Health table shows.
func (h *PipelineHandler) GetHealth(w http.ResponseWriter, r *http.Request) {
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run history store not wired")
		return
	}
	days := healthDefaultDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > healthMaxDays {
			replyError(w, http.StatusBadRequest, "days must be between 1 and 366")
			return
		}
		days = n
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	byID, err := h.runStore.HealthByPipeline(r.Context(), workspaceID, time.Now().AddDate(0, 0, -days), healthLastRuns)
	if err != nil {
		replyInternalError(w, h.logger, "routine health", err)
		return
	}
	routines, err := h.store.List(r.Context(), pipeline.ListFilters{WorkspaceID: workspaceID, IncludeHidden: true})
	if err != nil {
		replyInternalError(w, h.logger, "routine health: list", err)
		return
	}
	resp := routineHealthResponse{WindowDays: days, Routines: []routineHealthRow{}}
	for _, p := range routines {
		agg := byID[p.ID]
		if agg == nil {
			continue
		}
		row := routineHealthRow{Slug: p.Slug, LastOutcomes: agg.Outcomes, Runs: agg.Runs, SpendUSD: agg.SpendUSD}
		for _, o := range agg.Outcomes {
			switch o {
			case "completed":
				row.Finished++
				row.Succeeded++
			case "failed":
				row.Finished++
			}
		}
		if row.Finished > 0 {
			pct := (row.Succeeded*100 + row.Finished/2) / row.Finished
			row.SuccessPct = &pct
		}
		resp.Routines = append(resp.Routines, row)
	}
	writeJSON(w, http.StatusOK, resp)
}
