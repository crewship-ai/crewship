package api

// Crew monthly budget: the one limit the Crew settings Budget card sets.
// budget_limits is enforced by admission and run preflight
// (internal/paymaster/budgets.go); this file is the missing writer and the
// reader for the crew-scope, month-window row.

import (
	"database/sql"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/crewship-ai/crewship/internal/paymaster"
)

type crewBudgetRequest struct {
	LimitUSD float64 `json:"limit_usd"`
	// Mode is soft (warn only), hard (stop at 100 %) or tiered (warn at
	// 80 %, stop at 100 %, the default).
	Mode string `json:"mode"`
}

type crewBudgetResponse struct {
	CrewID   string   `json:"crew_id"`
	Window   string   `json:"window"`
	LimitUSD *float64 `json:"limit_usd"`
	Mode     string   `json:"mode,omitempty"`
	SpentUSD float64  `json:"spent_usd"`
	// State is none (no limit), ok, warn or exceeded.
	State string `json:"state"`
}

func (h *CrewHandler) budgetCrew(w http.ResponseWriter, r *http.Request) (workspaceID, crewID string, ok bool) {
	workspaceID = WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return "", "", false
	}
	crewID = r.PathValue("crewId")
	var found int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, crewID, workspaceID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "crew not found")
		return "", "", false
	}
	if err != nil {
		replyInternalError(w, h.logger, "load crew for budget", err)
		return "", "", false
	}
	return workspaceID, crewID, true
}

// budgetView reads the crew's own monthly row (a workspace or agent limit is
// not the crew's) with this month's spend.
func (h *CrewHandler) budgetView(r *http.Request, workspaceID, crewID string) (crewBudgetResponse, error) {
	out := crewBudgetResponse{CrewID: crewID, Window: "month", State: "none"}
	statuses, err := paymaster.Check(r.Context(), h.db, paymaster.Scope{WorkspaceID: workspaceID, CrewID: crewID})
	if err != nil {
		return out, err
	}
	for _, s := range statuses {
		if s.Budget.ScopeKind != paymaster.ScopeCrew || s.Budget.ScopeID != crewID || s.Budget.Window != paymaster.WindowMonth {
			continue
		}
		limit := s.LimitUSD
		out.LimitUSD = &limit
		out.Mode = string(s.Budget.Mode)
		out.SpentUSD = math.Round(s.SpentUSD*1e6) / 1e6
		out.State = string(s.State)
		break
	}
	return out, nil
}

// BudgetGet returns the crew's monthly limit and what it has spent this
// calendar month (UTC). ADMIN and above, like the spend views it sits beside.
// GET /api/v1/crews/{crewId}/budget
func (h *CrewHandler) BudgetGet(w http.ResponseWriter, r *http.Request) {
	ws, crewID, ok := h.budgetCrew(w, r)
	if !ok {
		return
	}
	out, err := h.budgetView(r, ws, crewID)
	if err != nil {
		replyInternalError(w, h.logger, "read crew budget", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// BudgetPut sets or replaces the crew's monthly limit.
// PUT /api/v1/crews/{crewId}/budget
func (h *CrewHandler) BudgetPut(w http.ResponseWriter, r *http.Request) {
	ws, crewID, ok := h.budgetCrew(w, r)
	if !ok {
		return
	}
	var req crewBudgetRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if math.IsNaN(req.LimitUSD) || math.IsInf(req.LimitUSD, 0) || req.LimitUSD <= 0 || req.LimitUSD > 1e9 {
		replyError(w, http.StatusBadRequest, "limit_usd must be greater than 0")
		return
	}
	mode := paymaster.ModeTiered
	switch req.Mode {
	case "":
	case string(paymaster.ModeSoft), string(paymaster.ModeHard), string(paymaster.ModeTiered):
		mode = paymaster.EnforcementMode(req.Mode)
	default:
		replyError(w, http.StatusBadRequest, "mode must be soft, hard or tiered")
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode, enabled, created_at, updated_at)
		VALUES (?, ?, 'crew', ?, 'month', ?, ?, 1, ?, ?)
		ON CONFLICT(workspace_id, scope_kind, scope_id, window)
		DO UPDATE SET limit_usd = excluded.limit_usd, mode = excluded.mode, enabled = 1, updated_at = excluded.updated_at`,
		uuid.NewString(), ws, crewID, req.LimitUSD, string(mode), now, now)
	if err != nil {
		replyInternalError(w, h.logger, "set crew budget", err)
		return
	}
	auditFromRequest(r, h.db, "crew.budget.set", "CREW", crewID, map[string]interface{}{
		"limit_usd": req.LimitUSD, "mode": string(mode), "window": "month",
	})
	out, err := h.budgetView(r, ws, crewID)
	if err != nil {
		replyInternalError(w, h.logger, "read crew budget", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// BudgetDelete removes the crew's monthly limit; clearing nothing succeeds.
// DELETE /api/v1/crews/{crewId}/budget
func (h *CrewHandler) BudgetDelete(w http.ResponseWriter, r *http.Request) {
	ws, crewID, ok := h.budgetCrew(w, r)
	if !ok {
		return
	}
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM budget_limits WHERE workspace_id = ? AND scope_kind = 'crew' AND scope_id = ? AND window = 'month'`, ws, crewID)
	if err != nil {
		replyInternalError(w, h.logger, "clear crew budget", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		auditFromRequest(r, h.db, "crew.budget.clear", "CREW", crewID, map[string]interface{}{"window": "month"})
	}
	out, err := h.budgetView(r, ws, crewID)
	if err != nil {
		replyInternalError(w, h.logger, "read crew budget", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
