package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

// mcp_tool_bindings backs the per-tool enable/disable affordance copied
// from Cursor (see CONNECTIONS.md §3.1, §5.5). Each row pins one tool
// (`tool_name`) on one MCP server (`mcp_server_id` + `mcp_server_scope`)
// to enabled / disabled. Default for missing row = enabled (so a tool
// the user has never seen the picker for still works); the row only
// materialises when the user explicitly toggles or the FE pushes a
// refreshed list.

type toolBindingResponse struct {
	ID          string  `json:"id"`
	ToolName    string  `json:"tool_name"`
	Description *string `json:"description"`
	Enabled     bool    `json:"enabled"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// ListCrewIntegrationTools returns every recorded tool binding for a
// crew-scoped MCP server. Tools the user has never interacted with
// won't be present yet — frontend supplements with the live discovery
// list when calling /tools/refresh.
//
// GET /api/v1/crews/{crewId}/integrations/{integrationId}/tools
func (h *IntegrationHandler) ListCrewIntegrationTools(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")
	serverID := r.PathValue("integrationId")

	// Verify the crew + server pair belongs to this workspace before
	// exposing tool data — same isolation check the CRUD handlers use.
	if err := h.assertCrewServerExists(r, workspaceID, crewID, serverID); err != nil {
		h.respondCrewServerErr(w, err)
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, tool_name, description, enabled, created_at, updated_at
		FROM mcp_tool_bindings
		WHERE mcp_server_id = ? AND mcp_server_scope = 'crew'
		ORDER BY tool_name ASC`, serverID)
	if err != nil {
		h.logger.Error("list mcp tool bindings", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	out := []toolBindingResponse{}
	for rows.Next() {
		var b toolBindingResponse
		var enabled int
		if err := rows.Scan(&b.ID, &b.ToolName, &b.Description, &enabled, &b.CreatedAt, &b.UpdatedAt); err != nil {
			h.logger.Error("scan mcp tool binding", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		b.Enabled = enabled != 0
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (mcp tool bindings)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, out)
}

type updateToolBindingRequest struct {
	Enabled     *bool   `json:"enabled"`
	Description *string `json:"description"`
}

// UpdateCrewIntegrationTool toggles a single tool binding. Upserts:
// missing row materialises with the requested state so the frontend
// doesn't need a separate "create then patch" handshake.
//
// PATCH /api/v1/crews/{crewId}/integrations/{integrationId}/tools/{toolName}
func (h *IntegrationHandler) UpdateCrewIntegrationTool(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	serverID := r.PathValue("integrationId")
	toolName := strings.TrimSpace(r.PathValue("toolName"))
	if toolName == "" {
		replyError(w, http.StatusBadRequest, "toolName required")
		return
	}

	if err := h.assertCrewServerExists(r, workspaceID, crewID, serverID); err != nil {
		h.respondCrewServerErr(w, err)
		return
	}

	var req updateToolBindingRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Enabled == nil && req.Description == nil {
		replyError(w, http.StatusBadRequest, "Provide at least one of: enabled, description")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Upsert via INSERT ... ON CONFLICT. Both enabled and description
	// flow as nullable params: when the request omits a field we pass
	// SQL NULL and the COALESCE in the UPDATE branch falls back to
	// the existing row's value, preserving prior toggles. The VALUES
	// clause uses COALESCE(?, 1) so a fresh row defaults enabled=1.
	//
	// CodeRabbit caught the prior bug where enabled=1 was always
	// supplied, so a description-only PATCH on a disabled tool would
	// silently flip it back on (excluded.enabled = 1 -> COALESCE picks
	// 1, not the existing 0).
	var enabledArg any // sql NULL when nil
	if req.Enabled != nil {
		if *req.Enabled {
			enabledArg = 1
		} else {
			enabledArg = 0
		}
	}
	desc := sql.NullString{}
	if req.Description != nil {
		desc.Valid = true
		desc.String = *req.Description
	}

	id := generateCUID()
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, description, enabled, created_at, updated_at)
		VALUES (?, ?, 'crew', ?, ?, COALESCE(?, 1), ?, ?)
		ON CONFLICT(mcp_server_id, mcp_server_scope, tool_name) DO UPDATE SET
			enabled = COALESCE(?, mcp_tool_bindings.enabled),
			description = COALESCE(excluded.description, mcp_tool_bindings.description),
			updated_at = excluded.updated_at`,
		id, serverID, toolName, desc, enabledArg, now, now, enabledArg)
	if err != nil {
		h.logger.Error("upsert mcp tool binding", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Read back to return the canonical row (id/created_at differ if
	// the upsert hit an existing record).
	var out toolBindingResponse
	var enabledOut int
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT id, tool_name, description, enabled, created_at, updated_at
		FROM mcp_tool_bindings
		WHERE mcp_server_id = ? AND mcp_server_scope = 'crew' AND tool_name = ?`,
		serverID, toolName).Scan(&out.ID, &out.ToolName, &out.Description, &enabledOut, &out.CreatedAt, &out.UpdatedAt); err != nil {
		h.logger.Error("read back mcp tool binding", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	out.Enabled = enabledOut != 0

	writeJSON(w, http.StatusOK, out)
}

type refreshToolsRequest struct {
	// Tools is the list discovered by the caller (frontend or a
	// future MCP gateway sync job). Each entry's enabled state is
	// preserved on existing rows; only newly seen tools default to
	// enabled. Empty list = no-op (does not wipe existing bindings).
	Tools []refreshToolEntry `json:"tools"`
}

type refreshToolEntry struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// RefreshCrewIntegrationTools accepts a list of tools the caller has
// just discovered (typically the frontend, after a successful test
// connection round-trip), and reconciles `mcp_tool_bindings`:
//
//   - new tool → row created with enabled = 1
//   - existing tool → description refreshed, enabled left untouched
//   - tool absent from payload → row left in place (we never auto-revoke)
//
// This decouples from the live MCP protocol: a future ticket can wire
// the sidecar to call mcp/list-tools server-side, but for MVP the FE
// already has the data after connecting and posts it back.
//
// POST /api/v1/crews/{crewId}/integrations/{integrationId}/tools/refresh
func (h *IntegrationHandler) RefreshCrewIntegrationTools(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	serverID := r.PathValue("integrationId")

	if err := h.assertCrewServerExists(r, workspaceID, crewID, serverID); err != nil {
		h.respondCrewServerErr(w, err)
		return
	}

	var req refreshToolsRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx (refresh tools)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			h.logger.Warn("refresh tools rollback", "error", rbErr)
		}
	}()

	created := 0
	updated := 0
	for _, t := range req.Tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		desc := sql.NullString{}
		if t.Description != nil {
			desc.Valid = true
			desc.String = *t.Description
		}
		// Keep enabled untouched on existing rows: enabled is NOT
		// updated in the conflict branch, so a previously toggled-off
		// tool stays off across refreshes. Description uses COALESCE
		// so an entry without a description doesn't NULL out an
		// existing one (CodeRabbit caught this).
		res, err := tx.ExecContext(r.Context(), `
			INSERT INTO mcp_tool_bindings (id, mcp_server_id, mcp_server_scope, tool_name, description, enabled, created_at, updated_at)
			VALUES (?, ?, 'crew', ?, ?, 1, ?, ?)
			ON CONFLICT(mcp_server_id, mcp_server_scope, tool_name) DO UPDATE SET
				description = COALESCE(excluded.description, mcp_tool_bindings.description),
				updated_at = excluded.updated_at`,
			generateCUID(), serverID, name, desc, now, now)
		if err != nil {
			h.logger.Error("upsert refresh tool", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			// SQLite returns 1 for both INSERT and ON CONFLICT update;
			// distinguish with a follow-up SELECT only if we need it.
			// For now lump created+updated under "touched" by using
			// timestamps: a cheap proxy is matching created_at == updated_at.
			updated++
		}
	}
	if err := tx.Commit(); err != nil {
		h.logger.Error("commit refresh tools", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Distinguish created vs. updated by counting rows where created_at
	// matches our `now` (just-inserted) vs. earlier (pre-existing).
	row := h.db.QueryRowContext(r.Context(), `
		SELECT
			SUM(CASE WHEN created_at = ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN created_at < ? THEN 1 ELSE 0 END)
		FROM mcp_tool_bindings
		WHERE mcp_server_id = ? AND mcp_server_scope = 'crew' AND updated_at = ?`,
		now, now, serverID, now)
	var c, u sql.NullInt64
	if err := row.Scan(&c, &u); err == nil {
		created = int(c.Int64)
		updated = int(u.Int64)
	}

	writeJSON(w, http.StatusOK, map[string]int{
		"created": created,
		"updated": updated,
		"total":   len(req.Tools),
	})
}

// assertCrewServerExists verifies that the (workspaceID, crewID,
// serverID) triple identifies a live (non-soft-deleted) crew MCP
// server. Returns sql.ErrNoRows on miss so callers can branch with
// errors.Is; any other error is a real DB failure (timeout, conn drop,
// etc.) and must NOT be conflated with "not found".
func (h *IntegrationHandler) assertCrewServerExists(r *http.Request, workspaceID, crewID, serverID string) error {
	var exists string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT cs.id FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id
		WHERE cs.id = ? AND cs.crew_id = ? AND c.workspace_id = ?
			AND cs.deleted_at IS NULL AND c.deleted_at IS NULL`,
		serverID, crewID, workspaceID).Scan(&exists)
	return err
}

// respondCrewServerErr discriminates "not found" from real DB failures.
// CodeRabbit flagged the previous behaviour where every error from
// assertCrewServerExists collapsed to 404 — that masks outages as
// false 404s and makes ops blind to genuine issues.
func (h *IntegrationHandler) respondCrewServerErr(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Crew integration not found")
		return
	}
	h.logger.Error("assert crew server exists", "error", err)
	replyError(w, http.StatusInternalServerError, "Internal server error")
}
