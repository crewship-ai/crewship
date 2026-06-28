package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// enqueueDeferredRun parks a delayed/debounced trigger in pending_runs.
// Always writes the HTTP response (scheduled receipt or error).
func (h *PipelineHandler) enqueueDeferredRun(w http.ResponseWriter, r *http.Request, workspaceID string, p *pipeline.Pipeline, body runRequestBody) {
	now := time.Now().UTC()

	// fire_at: a debounce trigger fires window-seconds after the latest
	// trigger (default 30s); a plain delay fires delay-seconds out.
	fireAt := now.Add(time.Duration(body.DelaySeconds) * time.Second)
	if body.DebounceKey != "" {
		window := body.DebounceWindowSecond
		if window <= 0 {
			window = 30
		}
		fireAt = now.Add(time.Duration(window) * time.Second)
	}

	var expiresAt *time.Time
	if body.TTLSeconds > 0 {
		e := now.Add(time.Duration(body.TTLSeconds) * time.Second)
		expiresAt = &e
	}
	var debounceMaxAt *time.Time
	if body.DebounceKey != "" && body.DebounceMaxSeconds > 0 {
		m := now.Add(time.Duration(body.DebounceMaxSeconds) * time.Second)
		debounceMaxAt = &m
	}

	inputsJSON := "{}"
	if len(body.Inputs) > 0 {
		if b, err := json.Marshal(body.Inputs); err == nil {
			inputsJSON = string(b)
		}
	}
	tagsJSON := "[]"
	if len(body.Tags) > 0 {
		if b, err := json.Marshal(body.Tags); err == nil {
			tagsJSON = string(b)
		}
	}

	store := pipeline.NewPendingRunStore(h.db)
	id, coalesced, err := store.Enqueue(r.Context(), pipeline.PendingRun{
		ID:            "pnd_" + generateCUID(),
		WorkspaceID:   workspaceID,
		PipelineID:    p.ID,
		PipelineSlug:  p.Slug,
		InputsJSON:    inputsJSON,
		TagsJSON:      tagsJSON,
		MetadataJSON:  marshalMetadata(body.Metadata),
		TierOverride:  body.TierOverride,
		Priority:      body.Priority,
		DebounceKey:   body.DebounceKey,
		FireAt:        fireAt,
		ExpiresAt:     expiresAt,
		DebounceMaxAt: debounceMaxAt,
	})
	if err != nil {
		h.logger.Error("enqueue deferred run", "error", err, "slug", p.Slug)
		replyError(w, http.StatusInternalServerError, "enqueue deferred run")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "SCHEDULED",
		"pending_id": id,
		"fire_at":    fireAt.Format(time.RFC3339Nano),
		"coalesced":  coalesced,
		"priority":   body.Priority,
	})
}

// ListPendingRuns returns the workspace's not-yet-fired deferred runs.
// GET /api/v1/workspaces/{workspaceId}/pipelines/pending
func (h *PipelineHandler) ListPendingRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	store := pipeline.NewPendingRunStore(h.db)
	rows, err := store.ListPending(r.Context(), workspaceID, 100)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "list pending runs")
		return
	}
	type dto struct {
		ID           string `json:"id"`
		PipelineSlug string `json:"pipeline_slug"`
		DebounceKey  string `json:"debounce_key,omitempty"`
		Priority     int    `json:"priority"`
		FireAt       string `json:"fire_at"`
	}
	out := make([]dto, 0, len(rows))
	for _, pr := range rows {
		out = append(out, dto{
			ID:           pr.ID,
			PipelineSlug: pr.PipelineSlug,
			DebounceKey:  pr.DebounceKey,
			Priority:     pr.Priority,
			FireAt:       pr.FireAt.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// CancelPendingRun cancels a not-yet-fired deferred run.
// POST /api/v1/workspaces/{workspaceId}/pipelines/pending/{pendingId}/cancel
func (h *PipelineHandler) CancelPendingRun(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	pendingID := r.PathValue("pendingId")
	if pendingID == "" {
		replyError(w, http.StatusBadRequest, "pendingId required")
		return
	}
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "db not wired")
		return
	}
	store := pipeline.NewPendingRunStore(h.db)
	ok, err := store.Cancel(r.Context(), workspaceID, pendingID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "cancel pending run")
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "pending run not found (already fired, expired, or cancelled)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cancelled": pendingID})
}
