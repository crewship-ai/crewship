package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/paymaster"
)

// PaymasterHandler serves cost-and-budget read endpoints. The write path
// lives inside the paymaster package's middleware which is wired into
// the LLM provider chain — nothing here accepts POST for ledger rows;
// they come exclusively from LLM calls.
type PaymasterHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewPaymasterHandler(db *sql.DB, logger *slog.Logger) *PaymasterHandler {
	return &PaymasterHandler{db: db, logger: logger}
}

// SpendByCrew serves GET /api/v1/paymaster/spend/by-crew?since=<d>
// Returns one row per crew with total cost + call count + token totals
// within the window (default last 7 days). Used by the Paymaster
// dashboard's "which crew is expensive" chart.
func (h *PaymasterHandler) SpendByCrew(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	since, until := parseWindow(r)
	rows, err := paymaster.SpendByCrew(r.Context(), h.db, workspaceID, since, until)
	if err != nil {
		h.logger.Error("paymaster spend-by-crew", "err", err)
		replyError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "since": since, "until": until})
}

// SpendByAgent returns per-agent totals within a crew. Crew ID comes
// from the URL path so the UI can drill down from the by-crew chart.
func (h *PaymasterHandler) SpendByAgent(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crew_id required")
		return
	}
	// Workspace isolation: paymaster.SpendByAgent filters by crew_id
	// alone, so without this guard a caller who learned another
	// workspace's crew ID could read that crew's LLM spend —
	// a cross-tenant read vulnerability. Confirm the crew belongs to
	// the caller's workspace before reading the ledger. 404 (not 403)
	// so the wrong-workspace case is indistinguishable from the
	// crew-doesn't-exist case — no existence leak.
	ok, err := crewBelongsToWorkspace(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("paymaster spend-by-agent: crew lookup failed", "err", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "crew lookup failed")
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "crew not found")
		return
	}
	since, until := parseWindow(r)
	rows, err := paymaster.SpendByAgent(r.Context(), h.db, crewID, since, until)
	if err != nil {
		h.logger.Error("paymaster spend-by-agent", "err", err)
		replyError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "crew_id": crewID})
}

// crewBelongsToWorkspace and missionBelongsToWorkspace centralise the
// scope pre-check used by the paymaster drill-downs. Returning
// (bool, error) lets callers render a flat 404 when the row truly
// doesn't exist without leaking cross-workspace existence, while
// surfacing real DB failures as 500 instead of silently masking them
// as "not found" (which hid transient outages in earlier revisions).
func crewBelongsToWorkspace(ctx context.Context, db *sql.DB, crewID, workspaceID string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM crews WHERE id = ? AND workspace_id = ?`, crewID, workspaceID).Scan(&n)
	if err == nil {
		return n == 1, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("lookup crew workspace: %w", err)
}

func missionBelongsToWorkspace(ctx context.Context, db *sql.DB, missionID, workspaceID string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM missions WHERE id = ? AND workspace_id = ?`, missionID, workspaceID).Scan(&n)
	if err == nil {
		return n == 1, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("lookup mission workspace: %w", err)
}

// SpendByMission returns per-mission totals. Used by the mission
// detail page to show "how much did this mission cost".
func (h *PaymasterHandler) SpendByMission(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	missionID := r.PathValue("missionId")
	if missionID == "" {
		replyError(w, http.StatusBadRequest, "mission_id required")
		return
	}
	// Same workspace-isolation check as SpendByAgent above —
	// SpendByMission filters by mission_id alone, so cross-tenant
	// reads would be possible without this gate. Flat 404 on miss
	// to avoid leaking existence across tenants.
	ok, err := missionBelongsToWorkspace(r.Context(), h.db, missionID, workspaceID)
	if err != nil {
		h.logger.Error("paymaster spend-by-mission: mission lookup failed", "err", err, "mission_id", missionID)
		replyError(w, http.StatusInternalServerError, "mission lookup failed")
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "mission not found")
		return
	}
	row, err := paymaster.SpendByMission(r.Context(), h.db, missionID)
	if err != nil {
		h.logger.Error("paymaster spend-by-mission", "err", err)
		replyError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"row": row, "mission_id": missionID})
}

// TopSpenders returns the top-N highest-cost scope records in the
// window. Default 10. Drives the "top spenders" tile on the dashboard.
func (h *PaymasterHandler) TopSpenders(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	since, _ := parseWindow(r)
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 100 {
			limit = n
		}
	}
	rows, err := paymaster.TopSpenders(r.Context(), h.db, workspaceID, limit, since)
	if err != nil {
		h.logger.Error("paymaster top-spenders", "err", err)
		replyError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "limit": limit, "since": since})
}

// SubscriptionUsage serves GET /api/v1/paymaster/subscriptions
// Returns one row per (subscription_plan, provider) with call counts,
// token totals, and last-used timestamp. Drives the "Subscription plans"
// panel on the Paymaster dashboard — the surface that finally tells
// operators which flat-rate credentials are active alongside the
// metered $-tracked spend.
//
// Behaves identically to the metered rollups for windowing: same
// since/until/range query params, same workspace isolation. Output rows
// carry NO $ figure because flat-rate cost is always $0 by construction;
// the UI is expected to render "no per-call cost tracking — flat-rate
// plan" rather than implying free.
func (h *PaymasterHandler) SubscriptionUsage(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	since, until := parseWindow(r)
	rows, err := paymaster.SubscriptionUsageByPlan(r.Context(), h.db, workspaceID, since, until)
	if err != nil {
		h.logger.Error("paymaster subscription-usage", "err", err)
		replyError(w, http.StatusInternalServerError, "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "since": since, "until": until})
}

// parseWindow accepts ?since=<RFC3339>&until=<RFC3339> or ?range=7d|24h|1h
// and returns sensible defaults (7 days back) when absent.
func parseWindow(r *http.Request) (time.Time, time.Time) {
	qs := r.URL.Query()
	now := time.Now().UTC()
	until := now
	since := now.Add(-7 * 24 * time.Hour)
	if v := qs.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = t
		}
	}
	if v := qs.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			until = t
		}
	}
	if v := qs.Get("range"); v != "" {
		switch v {
		case "1h":
			since = now.Add(-time.Hour)
		case "24h":
			since = now.Add(-24 * time.Hour)
		case "7d":
			since = now.Add(-7 * 24 * time.Hour)
		case "30d":
			since = now.Add(-30 * 24 * time.Hour)
		}
	}
	return since, until
}
