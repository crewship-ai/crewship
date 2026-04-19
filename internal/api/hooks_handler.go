package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/crewship-ai/crewship/internal/journal"
)

// HooksHandler serves the lifecycle-hook registry endpoints. The
// registration path is still a config-time operation (see
// internal/hooks/store.go:Register) — this handler is strictly read +
// enable/disable for operators.
type HooksHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

// NewHooksHandler wires a handler with a no-op journal; the router
// swaps in the real emitter via SetJournal once Router.journal has
// resolved.
func NewHooksHandler(db *sql.DB, logger *slog.Logger) *HooksHandler {
	return &HooksHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetJournal wires a journal emitter. A nil argument collapses back to
// the no-op so every emit path is safe to call.
func (h *HooksHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

// hookRow is the JSON projection returned to the CLI / UI. Matcher and
// HandlerConfig are rendered as-is so the caller can audit shell
// commands, HTTP URLs, and matcher predicates without a second round
// trip. Blocking is included because it materially changes how a hook
// fire affects the triggering operation (block vs warn).
type hookRow struct {
	ID            string         `json:"id"`
	WorkspaceID   string         `json:"workspace_id"`
	CrewID        string         `json:"crew_id,omitempty"`
	Event         string         `json:"event"`
	HandlerKind   string         `json:"handler_kind"`
	HandlerConfig map[string]any `json:"handler_config,omitempty"`
	Matcher       hooks.Matcher  `json:"matcher"`
	Enabled       bool           `json:"enabled"`
	Blocking      bool           `json:"blocking"`
	CreatedBy     string         `json:"created_by,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// List serves GET /api/v1/hooks[?crew_id=...]. Workspace-scoped — the
// WHERE clause always pins workspace_id to the caller's context, so a
// caller who learned another workspace's hook ID cannot discover it
// via this endpoint.
func (h *HooksHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	crewID := r.URL.Query().Get("crew_id")

	// Crew-filter guard: if the caller passes a crew_id that belongs to
	// another workspace, return a 404 (shape matches the "no rows" case
	// so we don't leak the crew's existence). crewBelongsToWorkspace
	// lives in paymaster_handler.go.
	if crewID != "" {
		ok, err := crewBelongsToWorkspace(r.Context(), h.db, crewID, workspaceID)
		if err != nil {
			h.logger.Error("hooks list: crew lookup failed", "err", err, "crew_id", crewID)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "crew lookup failed"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "crew not found"})
			return
		}
	}

	var (
		rows *sql.Rows
		err  error
	)
	const baseSelect = `SELECT id, workspace_id, crew_id, event, matcher,
		handler_kind, handler_config, blocking, enabled, created_by,
		created_at, updated_at FROM hooks_config WHERE workspace_id = ?`
	if crewID == "" {
		rows, err = h.db.QueryContext(r.Context(),
			baseSelect+` ORDER BY created_at DESC, id DESC`, workspaceID)
	} else {
		rows, err = h.db.QueryContext(r.Context(),
			baseSelect+` AND (crew_id IS NULL OR crew_id = ?) ORDER BY created_at DESC, id DESC`,
			workspaceID, crewID)
	}
	if err != nil {
		h.logger.Error("hooks list", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	defer rows.Close()

	// We reuse hooks.scanHook semantics here by hand because the package
	// doesn't export a scan helper (internal by design). The projection
	// matches what the CLI `crewship hooks list` renders and what the UI
	// settings panel needs.
	out := make([]hookRow, 0, 16)
	for rows.Next() {
		var (
			hk                                                hookRow
			crewNS, createdBy                                 sql.NullString
			matcherStr, handlerCfgStr, createdAt, updatedAt   string
			blockingInt, enabledInt                           int
		)
		if err := rows.Scan(
			&hk.ID,
			&hk.WorkspaceID,
			&crewNS,
			&hk.Event,
			&matcherStr,
			&hk.HandlerKind,
			&handlerCfgStr,
			&blockingInt,
			&enabledInt,
			&createdBy,
			&createdAt,
			&updatedAt,
		); err != nil {
			h.logger.Error("hooks list scan", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "scan failed"})
			return
		}
		hk.CrewID = crewNS.String
		hk.CreatedBy = createdBy.String
		hk.Enabled = enabledInt != 0
		hk.Blocking = blockingInt != 0
		hk.Matcher = hooks.Matcher{}
		hk.HandlerConfig = map[string]any{}
		if matcherStr != "" && matcherStr != "{}" {
			_ = json.Unmarshal([]byte(matcherStr), &hk.Matcher)
		}
		if handlerCfgStr != "" && handlerCfgStr != "{}" {
			_ = json.Unmarshal([]byte(handlerCfgStr), &hk.HandlerConfig)
		}
		if t, perr := time.Parse(time.RFC3339Nano, createdAt); perr == nil {
			hk.CreatedAt = t
		} else if t, perr := time.Parse("2006-01-02 15:04:05", createdAt); perr == nil {
			hk.CreatedAt = t
		}
		if t, perr := time.Parse(time.RFC3339Nano, updatedAt); perr == nil {
			hk.UpdatedAt = t
		} else if t, perr := time.Parse("2006-01-02 15:04:05", updatedAt); perr == nil {
			hk.UpdatedAt = t
		}
		out = append(out, hk)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("hooks list rows", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "iterate failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rows":  out,
		"count": len(out),
	})
}

// Enable serves POST /api/v1/hooks/{id}/enable. OWNER or ADMIN only —
// enabling a hook is a control-plane mutation that can invoke shell
// commands (for HandlerKindShell), route traffic to third-party HTTP
// endpoints, or dispatch subagents. Non-privileged members shouldn't
// flip those on.
func (h *HooksHandler) Enable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, true)
}

// Disable serves POST /api/v1/hooks/{id}/disable. Same permission gate
// as Enable.
func (h *HooksHandler) Disable(w http.ResponseWriter, r *http.Request) {
	h.setEnabled(w, r, false)
}

func (h *HooksHandler) setEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "toggling hooks requires OWNER or ADMIN role"})
		return
	}
	user := UserFromContext(r.Context())
	actorID := ""
	if user != nil {
		actorID = user.ID
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id required"})
		return
	}

	// Pre-fetch so we can include crew_id / event in the journal emit,
	// and so cross-tenant ID lookups surface as 404 (matching the
	// hooks.SetEnabled scoping contract documented in store.go).
	existing, err := hooks.Get(r.Context(), h.db, workspaceID, id)
	if err != nil {
		h.logger.Error("hooks get", "err", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "hook not found"})
		return
	}

	if err := hooks.SetEnabled(r.Context(), h.db, workspaceID, id, enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "hook not found"})
			return
		}
		h.logger.Error("hooks set enabled", "err", err, "id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "toggle failed"})
		return
	}

	verb := "disabled"
	if enabled {
		verb = "enabled"
	}
	_, _ = h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      existing.CrewID,
		Type:        journal.EntrySystemHookToggled,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     "hook " + id + " " + verb,
		Payload: map[string]any{
			"hook_id":      id,
			"enabled":      enabled,
			"event":        string(existing.Event),
			"handler_kind": string(existing.HandlerKind),
		},
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"enabled": enabled,
	})
}
