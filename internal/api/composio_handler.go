// Package api — Composio managed-integration handler.
//
// Crewship is retiring self-hosted MCP connector management in favour of the
// Composio platform. This handler exposes the project's Composio state to the
// dashboard/CLI so an operator can see the connector catalog (auth configs)
// and the per-user inventory of connected accounts — the read-only foundation
// the agent-binding + MCP-URL generation (later slices) build on.
//
// Isolation model: a Composio `user_id` owns a set of connected accounts.
// Crewship binds each agent to one user_id, so an agent only ever sees that
// user's accounts. The full inventory below is an OPERATOR view (gated on
// "read") and is never handed to an agent.
package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"sort"

	"github.com/crewship-ai/crewship/internal/composio"
	"github.com/crewship-ai/crewship/internal/config"
)

// ComposioHandler serves the /api/v1/integrations/composio/* routes.
type ComposioHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	client  *composio.Client // nil when the provider is not configured
	enabled bool
}

// NewComposioHandler wires a handler from the resolved Composio config. When
// the provider is disabled or has no API key, the handler still mounts but
// reports `enabled: false` so the UI can render a "not configured" state
// instead of erroring.
func NewComposioHandler(db *sql.DB, logger *slog.Logger, cfg *config.ComposioConfig) *ComposioHandler {
	h := &ComposioHandler{db: db, logger: logger}
	if cfg != nil && cfg.Enabled && cfg.APIKey != "" {
		h.client = composio.NewClient(cfg.APIKey, cfg.BaseURL)
		h.enabled = true
	}
	return h
}

// ── Response types ───────────────────────────────────────────────────────────

type composioInventoryResponse struct {
	Enabled     bool                    `json:"enabled"`
	AuthConfigs []composio.AuthConfig   `json:"auth_configs"`
	Users       []composioUserInventory `json:"users"`
}

// composioUserInventory groups one Composio user's connected accounts — the
// isolation unit Crewship binds agents against.
type composioUserInventory struct {
	UserID            string                       `json:"user_id"`
	ConnectedAccounts []composio.ConnectedAccount  `json:"connected_accounts"`
}

// ── ListInventory — GET /api/v1/integrations/composio/inventory ──────────────

// ListInventory returns the connector catalog (auth configs) plus all
// connected accounts grouped by Composio user_id. Read-gated: feature-flag
// signal / connected-account metadata isn't enumerated for non-members.
func (h *ComposioHandler) ListInventory(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}

	// Provider not configured: report disabled rather than failing, so the
	// dashboard renders the "connect Composio" empty state.
	if !h.enabled || h.client == nil {
		writeJSON(w, http.StatusOK, composioInventoryResponse{
			Enabled:     false,
			AuthConfigs: []composio.AuthConfig{},
			Users:       []composioUserInventory{},
		})
		return
	}

	ctx := r.Context()
	authConfigs, err := h.client.ListAuthConfigs(ctx)
	if err != nil {
		h.logger.Error("composio: list auth configs", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}
	accounts, err := h.client.ListConnectedAccounts(ctx)
	if err != nil {
		h.logger.Error("composio: list connected accounts", "error", err)
		writeProblem(w, r, http.StatusBadGateway, "Composio API error")
		return
	}

	resp := composioInventoryResponse{
		Enabled:     true,
		AuthConfigs: authConfigs,
		Users:       groupByUser(accounts),
	}
	if resp.AuthConfigs == nil {
		resp.AuthConfigs = []composio.AuthConfig{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// groupByUser folds connected accounts into per-user buckets, sorted by
// user_id for deterministic output (stable UI ordering + testable).
func groupByUser(accounts []composio.ConnectedAccount) []composioUserInventory {
	byUser := make(map[string][]composio.ConnectedAccount)
	for _, a := range accounts {
		byUser[a.UserID] = append(byUser[a.UserID], a)
	}
	users := make([]composioUserInventory, 0, len(byUser))
	for uid, accts := range byUser {
		users = append(users, composioUserInventory{UserID: uid, ConnectedAccounts: accts})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UserID < users[j].UserID })
	return users
}
