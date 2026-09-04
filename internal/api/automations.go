package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/automation"
)

// AutomationHandler is the workspace-scoped CRUD surface over the
// `automations` table — the rules that turn a journal event into a deferred
// routine run (internal/automation).
//
// Writes are ADMIN+ (declared on the route). An automation causes AUTONOMOUS
// routine execution across the whole workspace, on events its author may not
// produce; that is an administration capability, not a create-a-resource one,
// and it belongs on the same rung as editing a notification provider rather
// than filing an issue.
type AutomationHandler struct {
	db     *sql.DB
	store  *automation.Store
	logger *slog.Logger
	// refresh nudges the in-memory Registry after a write so a new rule
	// fires on the NEXT event rather than up to a minute later. Optional:
	// a Router built without the daemon's registry (tests, the CLI-only
	// build) simply relies on the 60s tick.
	refresh func(context.Context)
}

// NewAutomationHandler wires the handler.
func NewAutomationHandler(db *sql.DB, logger *slog.Logger) *AutomationHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &AutomationHandler{db: db, store: automation.NewStore(db), logger: logger}
}

// SetRefresh installs the registry-refresh hook. Called from cmd_start.go,
// which is where the Registry is constructed.
func (h *AutomationHandler) SetRefresh(fn func(context.Context)) { h.refresh = fn }

func (h *AutomationHandler) afterWrite(ctx context.Context) {
	if h.refresh != nil {
		h.refresh(ctx)
	}
}

// automationBody is the POST/PATCH wire shape. Every field is a pointer so
// PATCH can distinguish "leave alone" from "set to the zero value" — the
// difference between `automation disable` and `automation update --max-per-hour 0`.
type automationBody struct {
	Name            *string             `json:"name,omitempty"`
	Enabled         *bool               `json:"enabled,omitempty"`
	EventType       *string             `json:"event_type,omitempty"`
	Matcher         *automation.Matcher `json:"matcher,omitempty"`
	Action          *automation.Action  `json:"action,omitempty"`
	DebounceSeconds *int                `json:"debounce_seconds,omitempty"`
	MaxPerHour      *int                `json:"max_per_hour,omitempty"`
}

// List serves GET /api/v1/automations.
func (h *AutomationHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	rows, err := h.store.List(r.Context(), workspaceID)
	if err != nil {
		h.logger.Error("automation: list", "err", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": rows, "count": len(rows)})
}

// Create serves POST /api/v1/automations.
func (h *AutomationHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	var body automationBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	a := automation.Automation{
		WorkspaceID: workspaceID,
		Enabled:     true,
		ActionKind:  automation.ActionKindRoutine,
	}
	if body.Name != nil {
		a.Name = strings.TrimSpace(*body.Name)
	}
	if body.EventType != nil {
		a.EventType = strings.TrimSpace(*body.EventType)
	}
	if body.Enabled != nil {
		a.Enabled = *body.Enabled
	}
	if body.Matcher != nil {
		a.Matcher = *body.Matcher
	}
	if body.Action != nil {
		a.Action = *body.Action
	}
	if body.DebounceSeconds != nil {
		a.DebounceSeconds = *body.DebounceSeconds
	}
	if body.MaxPerHour != nil {
		a.MaxPerHour = *body.MaxPerHour
	}
	if u := UserFromContext(r.Context()); u != nil {
		a.CreatedBy = u.ID
	}
	if err := h.checkRoutineExists(r.Context(), workspaceID, a.Action.RoutineSlug); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	// event_type registration and matcher.payload_equals key validation
	// happen inside automation.Automation.Validate(), which Store.Create
	// calls — see its doc comment for why both checks live there now rather
	// than here.
	created, err := h.store.Create(r.Context(), a)
	if err != nil {
		if isAutomationUserError(err) {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.logger.Error("automation: create", "err", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "create failed")
		return
	}
	h.afterWrite(r.Context())
	writeJSON(w, http.StatusCreated, created)
}

// Patch serves PATCH /api/v1/automations/{id}. It is the single write behind
// `automation update`, `automation enable` and `automation disable`.
func (h *AutomationHandler) Patch(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	var body automationBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.EventType != nil {
		// Consistent with Create, which trims event_type before validating
		// it — a PATCH that only changes whitespace around an otherwise
		// valid type should not save a value Registered will reject.
		trimmed := strings.TrimSpace(*body.EventType)
		body.EventType = &trimmed
	}
	patch := automation.Patch{
		Name:            body.Name,
		Enabled:         body.Enabled,
		EventType:       body.EventType,
		Matcher:         body.Matcher,
		Action:          body.Action,
		DebounceSeconds: body.DebounceSeconds,
		MaxPerHour:      body.MaxPerHour,
	}
	// The routine-existence check is the one thing PATCH validates outside
	// automation.Automation.Validate() (see its doc comment: Validate is
	// pure and cannot make a DB read), and it is the one place PATCH must
	// validate ONLY what the body carries, not the rule's stored state. A
	// rule's routine can be soft-deleted after the rule is created —
	// Store.Update and Store.ListActive both deliberately tolerate that
	// dangling reference — so loading the current row to re-check a routine
	// slug the caller did not even send would turn `PATCH {"enabled":
	// false}` on such a rule into a 400 with no way out but DELETE. Only
	// check when this request is actually setting a routine.
	if body.Action != nil && body.Action.RoutineSlug != "" {
		if err := h.checkRoutineExists(r.Context(), workspaceID, body.Action.RoutineSlug); err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// event_type registration and matcher.payload_equals key validation of
	// the EFFECTIVE (merged) row happen inside Automation.Validate(), which
	// Store.Update calls after applying this same patch — no separate
	// load-and-merge needed here.
	updated, err := h.store.Update(r.Context(), workspaceID, id, patch)
	switch {
	case errors.Is(err, automation.ErrNotFound):
		replyError(w, http.StatusNotFound, "automation not found")
		return
	case err != nil && isAutomationUserError(err):
		replyError(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		h.logger.Error("automation: update", "err", err, "workspace_id", workspaceID, "id", id)
		replyError(w, http.StatusInternalServerError, "update failed")
		return
	}
	h.afterWrite(r.Context())
	writeJSON(w, http.StatusOK, updated)
}

// Delete serves DELETE /api/v1/automations/{id}. Soft-delete: the row stays
// so a run it caused can still explain where it came from.
func (h *AutomationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	switch err := h.store.Delete(r.Context(), workspaceID, id); {
	case errors.Is(err, automation.ErrNotFound):
		replyError(w, http.StatusNotFound, "automation not found")
		return
	case err != nil:
		h.logger.Error("automation: delete", "err", err, "workspace_id", workspaceID, "id", id)
		replyError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	h.afterWrite(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "id": id})
}

// checkRoutineExists rejects a routine_slug that names no live routine in
// this workspace. ListActive skips an automation whose routine cannot be
// resolved — the alternative is parking a run the dispatcher can never fire
// — so without this the failure surfaces as silence hours later rather than
// as a 400 at the moment of the typo.
//
// This is a DB read, so unlike the event_type and payload_equals checks (see
// automation.Automation.Validate) it cannot live inside Validate itself;
// callers must invoke it explicitly, and only when routineSlug is actually
// part of what they are setting (see Patch, which must not re-check a
// routine the caller did not send — a rule's routine is allowed to dangle
// between the time it is created and the time it is next edited).
func (h *AutomationHandler) checkRoutineExists(ctx context.Context, workspaceID, routineSlug string) error {
	if routineSlug == "" {
		return nil
	}
	var exists int
	err := h.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipelines WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`,
		workspaceID, routineSlug).Scan(&exists)
	if err != nil {
		return errors.New("could not verify the routine")
	}
	if exists == 0 {
		return errors.New("routine " + routineSlug + " does not exist in this workspace")
	}
	return nil
}

// isAutomationUserError reports whether err is the package's own validation
// refusal (which the caller can fix) rather than a database failure.
func isAutomationUserError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "automation: ")
}
