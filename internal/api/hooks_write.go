package api

// Write half of the hooks registry: POST / PATCH / DELETE /api/v1/hooks.
//
// The read + toggle endpoints live in hooks_handler.go. They shipped first
// and assumed registration was a config-time operation done in Go, which
// left hooks.Register with no production caller at all: internal/hooks had
// a complete dispatcher, matcher, and three handlers, and no supported way
// to get a row into hooks_config. These three handlers close that.
//
// Two gates matter here and are enforced in this file rather than at the
// route table, because neither reduces to a single workspace-role check:
//
//   - `event` must be one of the fourteen declared in internal/hooks. The
//     hooks_config schema CHECKs handler_kind but NOT event, so a typo
//     inserts cleanly and is then never selected by ListByEvent — the hook
//     lists, toggles, and looks healthy while being permanently dead. The
//     rejection message enumerates the legal values so the caller can fix
//     it without reading Go source.
//   - `handler_kind: shell` requires OWNER, not just the roleManage
//     (OWNER/ADMIN) the route declares. A shell hook runs a command on the
//     crewshipd host (internal/hooks/shell.go), so creating one is a
//     host-execution grant, a strictly larger power than the rest of the
//     ADMIN surface. Update carries the same gate: an http hook PATCHed
//     into a shell hook is the same grant by a longer road.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/hooks"
)

// maxHookBodyBytes caps a hook write body. Matcher and handler_config are
// free-form JSON, so without a bound a caller could push an arbitrarily
// large blob straight into a TEXT column that every Dispatch then parses.
const maxHookBodyBytes = 64 << 10

// hookWriteRequest is the wire shape for create and update. Every field is
// a pointer so PATCH can tell "omitted, keep what's there" apart from
// "explicitly set to the zero value" — without that, `{"event": "..."}`
// would silently clear blocking, enabled, and the matcher.
type hookWriteRequest struct {
	CrewID        *string         `json:"crew_id"`
	Event         *string         `json:"event"`
	Matcher       *hooks.Matcher  `json:"matcher"`
	HandlerKind   *string         `json:"handler_kind"`
	HandlerConfig *map[string]any `json:"handler_config"`
	Blocking      *bool           `json:"blocking"`
	Enabled       *bool           `json:"enabled"`
}

// decodeHookWrite reads and bounds the request body. Returns false having
// already written the response when the body is unusable.
func (h *HooksHandler) decodeHookWrite(w http.ResponseWriter, r *http.Request) (hookWriteRequest, bool) {
	var in hookWriteRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return in, false
	}
	return in, true
}

// validateHandlerKind checks the kind against the three the dispatcher can
// actually run, and applies the OWNER gate for shell. Returns false having
// written the response.
//
// The schema's CHECK constraint would also refuse an unknown kind, but as a
// 500 from a failed INSERT; naming the three legal values in a 400 is the
// difference between a usable API and a puzzle.
func validateHandlerKind(w http.ResponseWriter, kind, role string) bool {
	switch hooks.HandlerKind(kind) {
	case hooks.HandlerKindHTTP, hooks.HandlerKindSubagent:
		return true
	case hooks.HandlerKindShell:
		if role != "OWNER" {
			replyError(w, http.StatusForbidden,
				"handler_kind 'shell' requires OWNER role — shell hooks execute commands on the crewshipd host")
			return false
		}
		return true
	default:
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"invalid handler_kind %q (valid: shell, http, subagent)", kind))
		return false
	}
}

// validateEvent maps hooks.ValidateEvent onto a 400 whose body lists every
// legal event.
func validateEvent(w http.ResponseWriter, event string) bool {
	if err := hooks.ValidateEvent(hooks.Event(event)); err != nil {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"invalid event %q (valid: %s)", event, strings.Join(hooks.EventNames(), ", ")))
		return false
	}
	return true
}

// resolveHookCrew validates an optional crew scope against the caller's
// workspace. A crew from another tenant 404s with the same shape as a
// missing one, matching List's behaviour — no existence leak.
func (h *HooksHandler) resolveHookCrew(w http.ResponseWriter, r *http.Request, crewID, workspaceID string) bool {
	if crewID == "" {
		return true
	}
	ok, err := crewBelongsToWorkspace(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("hooks write: crew lookup failed", "err", err, "crew_id", crewID)
		replyError(w, http.StatusInternalServerError, "crew lookup failed")
		return false
	}
	if !ok {
		replyError(w, http.StatusNotFound, "crew not found")
		return false
	}
	return true
}

// Create serves POST /api/v1/hooks.
//
// The route declares roleManage (OWNER/ADMIN); shell additionally needs
// OWNER, enforced above. New hooks default to enabled=true: a registry you
// have to enable in a second call is a registry half of whose entries are
// silently off.
func (h *HooksHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	role := RoleFromContext(r.Context())

	in, ok := h.decodeHookWrite(w, r)
	if !ok {
		return
	}
	if in.Event == nil || *in.Event == "" {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"event is required (valid: %s)", strings.Join(hooks.EventNames(), ", ")))
		return
	}
	if !validateEvent(w, *in.Event) {
		return
	}
	if in.HandlerKind == nil || *in.HandlerKind == "" {
		replyError(w, http.StatusBadRequest, "handler_kind is required (valid: shell, http, subagent)")
		return
	}
	if !validateHandlerKind(w, *in.HandlerKind, role) {
		return
	}

	crewID := ""
	if in.CrewID != nil {
		crewID = strings.TrimSpace(*in.CrewID)
	}
	if !h.resolveHookCrew(w, r, crewID, workspaceID) {
		return
	}

	hk := hooks.Hook{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		Event:       hooks.Event(*in.Event),
		HandlerKind: hooks.HandlerKind(*in.HandlerKind),
		Enabled:     true,
		CreatedBy:   actorIDFromContext(r),
	}
	if in.Matcher != nil {
		hk.Matcher = *in.Matcher
	}
	if in.HandlerConfig != nil {
		hk.HandlerConfig = *in.HandlerConfig
	}
	if in.Blocking != nil {
		hk.Blocking = *in.Blocking
	}
	if in.Enabled != nil {
		hk.Enabled = *in.Enabled
	}

	// allowedShell mirrors the gate above rather than re-deriving it: the
	// store validates independently (store.go's comment explains why), and
	// passing role=="OWNER" keeps the two answers from drifting.
	id, err := hooks.Register(r.Context(), h.db, hk, role == "OWNER")
	if err != nil {
		h.writeStoreError(w, r, err, "create")
		return
	}

	WriteAuditLog(r.Context(), h.db, h.journal, "hook.create", "hook", id,
		actorIDFromContext(r), workspaceID, map[string]interface{}{
			"event":        *in.Event,
			"handler_kind": *in.HandlerKind,
			"crew_id":      crewID,
			"blocking":     hk.Blocking,
			"enabled":      hk.Enabled,
		})

	h.replyHook(w, r, workspaceID, id, http.StatusCreated)
}

// Update serves PATCH /api/v1/hooks/{id}.
//
// Semantics are patch, not put: the current row is loaded, only the
// supplied fields are overlaid, and the merged hook is written whole. That
// keeps the store's Update a single statement (no read-modify-write race
// inside the DB layer) while giving callers a partial-update API.
func (h *HooksHandler) Update(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	role := RoleFromContext(r.Context())
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "id required")
		return
	}

	in, ok := h.decodeHookWrite(w, r)
	if !ok {
		return
	}

	existing, err := hooks.Get(r.Context(), h.db, workspaceID, id)
	if err != nil {
		h.logger.Error("hooks update: lookup failed", "err", err, "id", id)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if existing == nil {
		replyError(w, http.StatusNotFound, "hook not found")
		return
	}

	merged := *existing
	if in.Event != nil {
		if !validateEvent(w, *in.Event) {
			return
		}
		merged.Event = hooks.Event(*in.Event)
	}
	if in.HandlerKind != nil {
		if !validateHandlerKind(w, *in.HandlerKind, role) {
			return
		}
		merged.HandlerKind = hooks.HandlerKind(*in.HandlerKind)
	} else if merged.HandlerKind == hooks.HandlerKindShell && role != "OWNER" {
		// Editing an existing shell hook is editing a host command, even
		// when the kind field is untouched. Same gate, same reason.
		replyError(w, http.StatusForbidden,
			"editing a shell hook requires OWNER role — shell hooks execute commands on the crewshipd host")
		return
	}
	if in.CrewID != nil {
		crewID := strings.TrimSpace(*in.CrewID)
		if !h.resolveHookCrew(w, r, crewID, workspaceID) {
			return
		}
		merged.CrewID = crewID
	}
	if in.Matcher != nil {
		merged.Matcher = *in.Matcher
	}
	if in.HandlerConfig != nil {
		merged.HandlerConfig = *in.HandlerConfig
	}
	if in.Blocking != nil {
		merged.Blocking = *in.Blocking
	}
	if in.Enabled != nil {
		merged.Enabled = *in.Enabled
	}

	if err := hooks.Update(r.Context(), h.db, workspaceID, merged, role == "OWNER"); err != nil {
		h.writeStoreError(w, r, err, "update")
		return
	}

	WriteAuditLog(r.Context(), h.db, h.journal, "hook.update", "hook", id,
		actorIDFromContext(r), workspaceID, map[string]interface{}{
			"event":        string(merged.Event),
			"handler_kind": string(merged.HandlerKind),
			"crew_id":      merged.CrewID,
			"blocking":     merged.Blocking,
			"enabled":      merged.Enabled,
		})

	h.replyHook(w, r, workspaceID, id, http.StatusOK)
}

// Delete serves DELETE /api/v1/hooks/{id}. Workspace-scoped, so a hook ID
// from another tenant 404s without revealing that it exists.
func (h *HooksHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "id required")
		return
	}

	// Read first so the audit row can record WHAT was deleted, not just
	// that something was. A deleted shell hook is exactly the record an
	// incident review needs, and after the DELETE it is unrecoverable.
	existing, err := hooks.Get(r.Context(), h.db, workspaceID, id)
	if err != nil {
		h.logger.Error("hooks delete: lookup failed", "err", err, "id", id)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if existing == nil {
		replyError(w, http.StatusNotFound, "hook not found")
		return
	}

	if err := hooks.Delete(r.Context(), h.db, workspaceID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "hook not found")
			return
		}
		h.logger.Error("hooks delete", "err", err, "id", id)
		replyError(w, http.StatusInternalServerError, "delete failed")
		return
	}

	WriteAuditLog(r.Context(), h.db, h.journal, "hook.delete", "hook", id,
		actorIDFromContext(r), workspaceID, map[string]interface{}{
			"event":        string(existing.Event),
			"handler_kind": string(existing.HandlerKind),
			"crew_id":      existing.CrewID,
		})

	writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// writeStoreError maps the store's sentinel errors onto status codes. The
// store validates independently of the handler (belt and braces), so these
// are the "handler gate missed something" paths — a 500 here would hide a
// real validation answer behind an opaque error.
func (h *HooksHandler) writeStoreError(w http.ResponseWriter, r *http.Request, err error, op string) {
	switch {
	case errors.Is(err, hooks.ErrShellHookNotAllowed):
		replyError(w, http.StatusForbidden,
			"handler_kind 'shell' requires OWNER role — shell hooks execute commands on the crewshipd host")
	case errors.Is(err, hooks.ErrUnknownEvent):
		replyError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, hooks.ErrEventCannotBlock):
		replyError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, hooks.ErrUnknownHandlerKind):
		replyError(w, http.StatusBadRequest, "invalid handler_kind (valid: shell, http, subagent)")
	case errors.Is(err, sql.ErrNoRows):
		replyError(w, http.StatusNotFound, "hook not found")
	case strings.Contains(err.Error(), "requires handler_config"):
		// The store owns the per-kind handler_config shape rules (url for
		// http, command for shell); surfacing its message verbatim keeps
		// one copy of that contract rather than two that drift.
		replyError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "hooks: "))
	default:
		h.logger.Error("hooks "+op, "err", err)
		replyError(w, http.StatusInternalServerError, op+" failed")
	}
}

// replyHook re-reads the row and renders it in the same projection List
// uses, so create/update/list responses are one shape the CLI and UI can
// share a decoder for.
func (h *HooksHandler) replyHook(w http.ResponseWriter, r *http.Request, workspaceID, id string, status int) {
	hk, err := hooks.Get(r.Context(), h.db, workspaceID, id)
	if err != nil || hk == nil {
		h.logger.Error("hooks: read-back after write failed", "err", err, "id", id)
		// The write itself succeeded; report that rather than a 500 the
		// caller might retry into a duplicate.
		writeJSON(w, status, map[string]any{"id": id})
		return
	}
	writeJSON(w, status, hookToRow(*hk))
}

// hookToRow projects the store's Hook onto the JSON shape List emits.
func hookToRow(hk hooks.Hook) hookRow {
	cfg := hk.HandlerConfig
	if cfg == nil {
		cfg = map[string]any{}
	}
	return hookRow{
		ID:            hk.ID,
		WorkspaceID:   hk.WorkspaceID,
		CrewID:        hk.CrewID,
		Event:         string(hk.Event),
		HandlerKind:   string(hk.HandlerKind),
		HandlerConfig: cfg,
		Matcher:       hk.Matcher,
		Enabled:       hk.Enabled,
		Blocking:      hk.Blocking,
		CreatedBy:     hk.CreatedBy,
		CreatedAt:     hk.CreatedAt,
		UpdatedAt:     hk.UpdatedAt,
	}
}

// actorIDFromContext pulls the acting user's id, empty for a token-only
// principal. WriteAuditLog turns an empty id into a NULL FK + system actor.
func actorIDFromContext(r *http.Request) string {
	if u := UserFromContext(r.Context()); u != nil {
		return u.ID
	}
	return ""
}
