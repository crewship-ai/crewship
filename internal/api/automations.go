package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
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

// eventTypePattern is a shape check, not a membership check: the journal has
// 117 entry types and no exported registry of them, so the honest guarantee
// here is "this looks like an entry type", not "this is one". A typo produces
// a rule that never fires, which is why the docs tell you to confirm the type
// with `crewship journal --type <t>` before creating the automation.
var eventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

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
	if err := h.validate(r.Context(), workspaceID, a.EventType, a.Action.RoutineSlug); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
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
	patch := automation.Patch{
		Name:            body.Name,
		Enabled:         body.Enabled,
		EventType:       body.EventType,
		Matcher:         body.Matcher,
		Action:          body.Action,
		DebounceSeconds: body.DebounceSeconds,
		MaxPerHour:      body.MaxPerHour,
	}
	eventType, routine := "", ""
	if body.EventType != nil {
		eventType = *body.EventType
	}
	if body.Action != nil {
		routine = body.Action.RoutineSlug
	}
	if err := h.validate(r.Context(), workspaceID, eventType, routine); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
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

// validate rejects the two mistakes that would otherwise produce a rule that
// looks saved and never fires: an event type that is not shaped like a journal
// entry type, and a routine slug that does not exist in this workspace.
//
// The second check matters more than it looks. ListActive skips an automation
// whose routine cannot be resolved — the alternative is parking a run the
// dispatcher can never fire — so without this the failure surfaces as silence
// hours later rather than as a 400 at the moment of the typo.
func (h *AutomationHandler) validate(ctx context.Context, workspaceID, eventType, routineSlug string) error {
	if eventType != "" && !eventTypePattern.MatchString(eventType) {
		return errors.New("event_type must look like a journal entry type, e.g. mission.status_change")
	}
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
