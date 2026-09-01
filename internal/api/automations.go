package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
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

// maxEventTypeSuggestions bounds how many alternatives an unregistered-type
// error names. journal.AllEntryTypes has ~129 entries; naming all of them in
// a 400 body would bury the useful part of the message.
const maxEventTypeSuggestions = 8

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
	if err := h.validate(r.Context(), workspaceID, a.EventType, a.Action.RoutineSlug, a.Matcher); err != nil {
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
	// PATCH is sparse, but event_type and matcher.payload_equals only mean
	// anything TOGETHER: a request that patches only the matcher must still
	// be checked against the rule's effective (already-stored) event type,
	// and a request that patches only event_type must still be checked
	// against the rule's effective (already-stored) matcher. Loading the
	// current row and merging the patch onto it — the same merge Store.Update
	// does internally before its own Validate() — is what makes that
	// possible; validating only the fields present in THIS request body
	// would silently skip validation whenever a caller split a rule's
	// event_type and matcher across two PATCH calls.
	cur, err := h.store.Get(r.Context(), workspaceID, id)
	switch {
	case errors.Is(err, automation.ErrNotFound):
		replyError(w, http.StatusNotFound, "automation not found")
		return
	case err != nil:
		h.logger.Error("automation: patch load", "err", err, "workspace_id", workspaceID, "id", id)
		replyError(w, http.StatusInternalServerError, "load failed")
		return
	}
	eventType, routine, matcher := cur.EventType, cur.Action.RoutineSlug, cur.Matcher
	if body.EventType != nil {
		eventType = *body.EventType
	}
	if body.Action != nil {
		routine = body.Action.RoutineSlug
	}
	if body.Matcher != nil {
		matcher = *body.Matcher
	}
	if err := h.validate(r.Context(), workspaceID, eventType, routine, matcher); err != nil {
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

// validate rejects the three mistakes that would otherwise produce a rule
// that looks saved and never fires: an event type that names no real journal
// entry type, a matcher.payload_equals key that entry type's emitter never
// writes, and a routine slug that does not exist in this workspace.
//
// The routine check matters more than it looks. ListActive skips an
// automation whose routine cannot be resolved — the alternative is parking a
// run the dispatcher can never fire — so without this the failure surfaces
// as silence hours later rather than as a 400 at the moment of the typo. The
// event-type and payload-key checks exist for exactly the same reason,
// closing PRD-ISSUES-AND-ROUTINES-2026 §A3: before this, event_type was
// validated by SHAPE ONLY (a regex) and payload_equals keys were never
// validated at all, so a well-formed rule that could never match anything
// saved successfully.
func (h *AutomationHandler) validate(ctx context.Context, workspaceID, eventType, routineSlug string, matcher automation.Matcher) error {
	if eventType != "" {
		et := journal.EntryType(eventType)
		if !journal.Registered(et) {
			return fmt.Errorf("event_type %q is not a registered journal entry type; %s",
				eventType, eventTypeHint(eventType))
		}
		if err := validatePayloadEqualsKeys(et, matcher.PayloadEquals); err != nil {
			return err
		}
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

// eventTypeHint builds the "name the valid alternatives" half of an
// unregistered-event_type error. It prefers types sharing the same namespace
// as the rejected value (a typo of "mission.status_change" should surface
// other mission.* types, not a random sample of the whole registry) and
// falls back to the first few types alphabetically when nothing shares a
// namespace — plus, either way, a pointer to the full closed list and to
// confirming a specific type against real history.
func eventTypeHint(rejected string) string {
	namespace := rejected
	if i := strings.IndexByte(rejected, '.'); i >= 0 {
		namespace = rejected[:i]
	}
	var suggestions []string
	for _, t := range journal.AllEntryTypes {
		if strings.HasPrefix(string(t), namespace+".") {
			suggestions = append(suggestions, string(t))
		}
	}
	sort.Strings(suggestions)
	how := fmt.Sprintf("run `crewship journal --type <t> --lines 1` to confirm one exists, "+
		"or see journal.AllEntryTypes for the full closed list of %d types", len(journal.AllEntryTypes))
	if len(suggestions) == 0 {
		suggestions = make([]string, 0, maxEventTypeSuggestions)
		for _, t := range journal.AllEntryTypes {
			suggestions = append(suggestions, string(t))
			if len(suggestions) == maxEventTypeSuggestions {
				break
			}
		}
		return fmt.Sprintf("no registered type starts with %q — a few examples: %s (%s)",
			namespace, strings.Join(suggestions, ", "), how)
	}
	truncated := len(suggestions) > maxEventTypeSuggestions
	if truncated {
		suggestions = suggestions[:maxEventTypeSuggestions]
	}
	suffix := ""
	if truncated {
		suffix = ", …"
	}
	return fmt.Sprintf("registered types starting with %q: %s%s (%s)",
		namespace+".", strings.Join(suggestions, ", "), suffix, how)
}

// validatePayloadEqualsKeys rejects any matcher.payload_equals key that is
// not a documented payload field of eventType, per automation.KnownPayloadKeys.
//
// KnownPayloadKeys is a curated subset, not every registered event type (see
// its doc comment for why an exhaustive, generated version was rejected) —
// automation.ValidPayloadKey returns true with no opinion for an eventType
// it does not catalogue, so this only ever rejects a key against a type this
// package has actually verified the emitter for.
func validatePayloadEqualsKeys(eventType journal.EntryType, payloadEquals map[string]any) error {
	if len(payloadEquals) == 0 {
		return nil
	}
	keys := make([]string, 0, len(payloadEquals))
	for k := range payloadEquals {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic: report the same offending key every time, not a random map order
	for _, k := range keys {
		if automation.ValidPayloadKey(eventType, k) {
			continue
		}
		known, ok := automation.KnownPayloadKeys[eventType]
		if !ok {
			// Unreachable in practice — ValidPayloadKey returns true for an
			// uncatalogued type — but keep the branch honest rather than
			// panic-indexing a nil slice if that contract ever changes.
			return fmt.Errorf("matcher.payload_equals key %q is not a documented payload field of %q", k, eventType)
		}
		return fmt.Errorf("matcher.payload_equals key %q is not a documented payload field of %q; "+
			"known keys: %s — read one real entry first with `crewship journal --type %s --lines 1 --format json`",
			k, eventType, strings.Join(known, ", "), eventType)
	}
	return nil
}

// isAutomationUserError reports whether err is the package's own validation
// refusal (which the caller can fix) rather than a database failure.
func isAutomationUserError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "automation: ")
}
