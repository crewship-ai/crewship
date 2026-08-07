package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
)

// previewWindow is how much history a preview judges a rule against. Long
// enough that a quiet workspace still has something to say, short enough
// that "0 matched" means the rule is wrong rather than that the window was
// empty — and the response reports `scanned` so the reader can tell those
// two apart themselves.
const previewWindow = 7 * 24 * time.Hour

const previewLimit = 500

type previewRequest struct {
	// EventType and Matcher describe a rule that may not exist yet, so an
	// author can check a predicate BEFORE saving it. When AutomationID is
	// set, the saved rule's own event type and matcher are used instead.
	AutomationID string              `json:"automation_id,omitempty"`
	EventType    string              `json:"event_type,omitempty"`
	Matcher      *automation.Matcher `json:"matcher,omitempty"`
}

// PreviewMatch serves POST /api/v1/automations/preview.
//
// It answers the question the create command's own help text admits it
// cannot: would this rule ever fire? A matcher is written blind today —
// save it, wait, and notice nothing happened — and the shipped example
// predicated on a payload key the event does not carry, so the first rule
// most readers built did nothing and said nothing.
//
// Read-only. It replays entries already written; it never enqueues a run.
func (h *AutomationHandler) PreviewMatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}

	var req previewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	eventType, matcher := req.EventType, automation.Matcher{}
	if req.Matcher != nil {
		matcher = *req.Matcher
	}
	if req.AutomationID != "" {
		saved, err := h.store.Get(r.Context(), workspaceID, req.AutomationID)
		if errors.Is(err, automation.ErrNotFound) {
			replyError(w, http.StatusNotFound, "automation not found")
			return
		}
		if err != nil {
			h.logger.Error("automation preview: load", "err", err, "id", req.AutomationID)
			replyError(w, http.StatusInternalServerError, "load failed")
			return
		}
		eventType, matcher = saved.EventType, saved.Matcher
	}
	if eventType == "" {
		replyError(w, http.StatusBadRequest, "event_type is required (or automation_id to preview a saved rule)")
		return
	}

	entries, _, err := journal.List(r.Context(), h.db, journal.Query{
		WorkspaceID: workspaceID,
		Types:       []journal.EntryType{journal.EntryType(eventType)},
		Since:       time.Now().UTC().Add(-previewWindow),
		Limit:       previewLimit,
	})
	if err != nil {
		h.logger.Error("automation preview: journal", "err", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "journal read failed")
		return
	}

	res := automation.Preview(matcher, eventType, entries)
	writeJSON(w, http.StatusOK, map[string]any{
		"event_type":    eventType,
		"window_hours":  int(previewWindow.Hours()),
		"scanned":       res.Scanned,
		"matched":       res.Matched,
		"samples":       res.Samples,
		"top_rejection": res.TopRejection,
	})
}
