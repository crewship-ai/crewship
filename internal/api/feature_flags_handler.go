// Package api — Feature Flag REST handler.
//
// Feature flags are an instance-global toggle surface: an ADMIN defines a
// flag with a default `enabled` and percentage rollout, then individual
// workspaces may attach an override row (`feature_flag_overrides`) that
// flips the flag for that workspace only.
//
// RBAC mapping (the canRole helper only knows "read", "create", "update",
// "manage", "delete" — it has no separate "admin" action). The spec calls
// out two privilege tiers:
//
//   - "ADMIN-only" (instance-global flag definition CRUD) → mapped to
//     `requireRole(w, r, "manage")` which gates on OWNER+ADMIN. OWNER is
//     strictly more privileged than ADMIN in the workspace role hierarchy,
//     so allowing OWNER to administer flags is consistent with the broader
//     RBAC tier convention used everywhere else in this package.
//
//   - "OWNER/ADMIN of workspace" (per-workspace override CRUD) → same
//     `requireRole(w, r, "manage")` gate. The set of allowed roles is
//     literally {OWNER, ADMIN}, which is exactly what "manage" expands to.
//
// If we ever need to draw a real line between "instance admin" and
// "workspace admin" (e.g. a separate global role), that's a follow-up —
// adding a new `canRole` action is the right place, not branching here.
package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint
// violation. The modernc.org driver surfaces these as wrapped errors
// whose string contains "UNIQUE constraint failed". Pinning on the
// substring keeps this resilient to wrapper changes — sql.ErrNoRows
// is the only typed error we get out of database/sql, so constraint
// codes have to be matched textually.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed: UNIQUE")
}

// FeatureFlagHandler implements CRUD for feature flag definitions plus
// per-workspace override upsert/delete.
type FeatureFlagHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewFeatureFlagHandler constructs a FeatureFlagHandler.
func NewFeatureFlagHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *FeatureFlagHandler {
	return &FeatureFlagHandler{db: db, hub: hub, logger: logger}
}

// ── Response types ─────────────────────────────────────────────────────────

// featureFlagResponse is the wire shape returned by List/Create/Update.
// `OverrideEnabled` is populated only when the current workspace has an
// override row attached; otherwise it stays nil (= inherit instance default).
type featureFlagResponse struct {
	ID              string  `json:"id"`
	Key             string  `json:"key"`
	Description     *string `json:"description"`
	Enabled         bool    `json:"enabled"`
	Percentage      int     `json:"percentage"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	OverrideEnabled *bool   `json:"override_enabled,omitempty"`
}

// ── 1. List — GET /api/v1/feature-flags ────────────────────────────────────

// List returns every flag definition plus, if the request carries a
// workspace context, the current workspace's per-flag override (or null
// when no override row exists for that flag).
func (h *FeatureFlagHandler) List(w http.ResponseWriter, r *http.Request) {
	// "read" gate keeps the empty-role / no-membership case out — feature
	// flags can carry product-strategy signal so we don't enumerate them
	// for callers who aren't members of any workspace role. canRole("")
	// returns false for every action, which is exactly the contract the
	// RBAC matrix test pins.
	if !requireRole(w, r, "read") {
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	// LEFT JOIN so flags without an override still come back.
	// Override row is filtered by current workspace.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT f.id, f.key, f.description, f.enabled, f.percentage,
		       f.created_at, f.updated_at,
		       o.enabled AS override_enabled
		FROM feature_flags f
		LEFT JOIN feature_flag_overrides o
		  ON o.flag_id = f.id AND o.workspace_id = ?
		ORDER BY f.key ASC`, wsID)
	if err != nil {
		h.logger.Error("list feature flags", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []featureFlagResponse
	for rows.Next() {
		var (
			ff           featureFlagResponse
			enabledInt   int
			overrideNull sql.NullInt64
		)
		if err := rows.Scan(
			&ff.ID, &ff.Key, &ff.Description, &enabledInt, &ff.Percentage,
			&ff.CreatedAt, &ff.UpdatedAt, &overrideNull,
		); err != nil {
			h.logger.Error("scan feature flag", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		ff.Enabled = enabledInt != 0
		if overrideNull.Valid {
			b := overrideNull.Int64 != 0
			ff.OverrideEnabled = &b
		}
		result = append(result, ff)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (feature flags)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []featureFlagResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. Create — POST /api/v1/feature-flags ─────────────────────────────────

// Create inserts a new instance-global flag definition.
// ADMIN-only (see package doc for RBAC mapping rationale).
func (h *FeatureFlagHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	var req struct {
		Key         string  `json:"key"`
		Description *string `json:"description"`
		Enabled     bool    `json:"enabled"`
		Percentage  int     `json:"percentage"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key is required")
		return
	}
	if req.Percentage < 0 || req.Percentage > 100 {
		writeProblem(w, r, http.StatusBadRequest, "percentage must be between 0 and 100")
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	enabledInt := 0
	if req.Enabled {
		enabledInt = 1
	}

	// Race-free uniqueness: let the UNIQUE(key) constraint do the
	// check inside the INSERT. The previous "SELECT then INSERT"
	// pattern raced under concurrent creates — two callers could
	// both observe "key not found" and then both attempt to insert,
	// surfacing a 500 instead of the intended 409 to the loser.
	// SQLite reports the violation through the modernc.org driver as
	// either a "constraint failed" message; either is enough to map
	// to a clean 409.
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO feature_flags (id, key, description, enabled, percentage, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, req.Key, req.Description, enabledInt, req.Percentage, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			writeProblem(w, r, http.StatusConflict, "feature flag with this key already exists")
			return
		}
		h.logger.Error("insert feature flag", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := featureFlagResponse{
		ID:          id,
		Key:         req.Key,
		Description: req.Description,
		Enabled:     req.Enabled,
		Percentage:  req.Percentage,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Instance-global broadcast: workspace omitted because the change
	// affects every workspace's default. Clients should refetch.
	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()),
		"feature_flag.created", map[string]string{"key": req.Key})

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Update — PATCH /api/v1/feature-flags/{key} ──────────────────────────

// Update mutates one or more fields of an existing flag definition.
// ADMIN-only.
func (h *FeatureFlagHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key path parameter is required")
		return
	}

	// Resolve key → id so the UPDATE below can use the primary key
	// (cheaper, and lets us return a structured 404 on misses).
	var id string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM feature_flags WHERE key = ?`, key).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Feature flag not found")
			return
		}
		h.logger.Error("get feature flag for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	var req struct {
		Description *string `json:"description"`
		Enabled     *bool   `json:"enabled"`
		Percentage  *int    `json:"percentage"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()
	if req.Description != nil {
		if *req.Description == "" {
			ub.SetNull("description")
		} else {
			ub.Set("description", *req.Description)
		}
	}
	if req.Enabled != nil {
		v := 0
		if *req.Enabled {
			v = 1
		}
		ub.Set("enabled", v)
	}
	if req.Percentage != nil {
		if *req.Percentage < 0 || *req.Percentage > 100 {
			writeProblem(w, r, http.StatusBadRequest, "percentage must be between 0 and 100")
			return
		}
		ub.Set("percentage", *req.Percentage)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("feature_flags", "id = ?", id)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update feature flag", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()),
		"feature_flag.updated", map[string]string{"key": key})

	// Return the freshly-updated row so the client doesn't need a follow-up
	// GET to display state. Include override status for the current workspace
	// to match List's response shape.
	wsID := WorkspaceIDFromContext(r.Context())
	var (
		ff           featureFlagResponse
		enabledInt   int
		overrideNull sql.NullInt64
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT f.id, f.key, f.description, f.enabled, f.percentage,
		       f.created_at, f.updated_at,
		       o.enabled AS override_enabled
		FROM feature_flags f
		LEFT JOIN feature_flag_overrides o
		  ON o.flag_id = f.id AND o.workspace_id = ?
		WHERE f.id = ?`, wsID, id).Scan(
		&ff.ID, &ff.Key, &ff.Description, &enabledInt, &ff.Percentage,
		&ff.CreatedAt, &ff.UpdatedAt, &overrideNull,
	)
	if err != nil {
		h.logger.Error("read updated feature flag", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	ff.Enabled = enabledInt != 0
	if overrideNull.Valid {
		b := overrideNull.Int64 != 0
		ff.OverrideEnabled = &b
	}

	writeJSON(w, http.StatusOK, ff)
}

// ── 4. Delete — DELETE /api/v1/feature-flags/{key} ─────────────────────────

// Delete removes a flag definition. The ON DELETE CASCADE on
// feature_flag_overrides means all per-workspace override rows go too.
// ADMIN-only.
func (h *FeatureFlagHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key path parameter is required")
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM feature_flags WHERE key = ?`, key)
	if err != nil {
		h.logger.Error("delete feature flag", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete feature flag rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Feature flag not found")
		return
	}

	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()),
		"feature_flag.deleted", map[string]string{"key": key})

	w.WriteHeader(http.StatusNoContent)
}

// ── 5. UpsertOverride — PUT /api/v1/feature-flags/{key}/override ───────────

// UpsertOverride creates or replaces the current workspace's override
// row for the named flag. Body: {"enabled": <bool>}. Idempotent — a
// repeated PUT with the same body is a no-op semantics-wise (updates the
// same row in place via the UNIQUE(flag_id, workspace_id) constraint).
//
// OWNER/ADMIN of the workspace required.
func (h *FeatureFlagHandler) UpsertOverride(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key path parameter is required")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace context required for override")
		return
	}

	// Pointer so we can distinguish absent (rejected) from explicit
	// false. The previous non-pointer decode accepted `{}` and
	// silently flipped the flag off, which is a serious foot-gun for
	// a privileged endpoint — an operator that meant to set
	// `enabled: true` and mis-typed the body would silently disable
	// the flag for the whole workspace.
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Enabled == nil {
		writeProblem(w, r, http.StatusBadRequest, "request body must include an explicit boolean 'enabled' field")
		return
	}
	enabledVal := *req.Enabled

	// Resolve flag key → id (flag must exist; we don't auto-create).
	var flagID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM feature_flags WHERE key = ?`, key).Scan(&flagID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Feature flag not found")
			return
		}
		h.logger.Error("get feature flag for override", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	enabledInt := 0
	if enabledVal {
		enabledInt = 1
	}

	// SQLite UPSERT against the UNIQUE(flag_id, workspace_id) constraint.
	// On conflict we just flip `enabled` — no need to touch created_at.
	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO feature_flag_overrides (id, flag_id, workspace_id, enabled, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(flag_id, workspace_id) DO UPDATE SET enabled = excluded.enabled`,
		id, flagID, wsID, enabledInt, now)
	if err != nil {
		h.logger.Error("upsert feature flag override", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "feature_flag.override_set",
		map[string]string{"key": key})

	writeJSON(w, http.StatusOK, map[string]any{
		"key":     key,
		"enabled": enabledVal,
	})
}

// ── 6. DeleteOverride — DELETE /api/v1/feature-flags/{key}/override ────────

// DeleteOverride removes the current workspace's override row, reverting
// the flag's effective value to the instance-global default.
// OWNER/ADMIN of the workspace required.
func (h *FeatureFlagHandler) DeleteOverride(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key path parameter is required")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace context required for override")
		return
	}

	// Resolve flag key → id first to return a precise 404 when the flag
	// itself is missing (vs the override row simply not existing).
	var flagID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM feature_flags WHERE key = ?`, key).Scan(&flagID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Feature flag not found")
			return
		}
		h.logger.Error("get feature flag for delete override", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM feature_flag_overrides WHERE flag_id = ? AND workspace_id = ?`,
		flagID, wsID)
	if err != nil {
		h.logger.Error("delete feature flag override", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete feature flag override rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		// No override existed for this workspace+flag pair. Treat as a
		// success (idempotent inherit) but signal via 204 NoContent so
		// the caller can distinguish from a 200 OK that did delete a row
		// if they squint at the response code. We choose 204 in both
		// cases to keep the contract simple — "after this call, the
		// override does not exist".
		w.WriteHeader(http.StatusNoContent)
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "feature_flag.override_cleared",
		map[string]string{"key": key})

	w.WriteHeader(http.StatusNoContent)
}
