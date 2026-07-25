package api

// Operator surface for cross-run routine state (pipeline_routine_state, #1420).
//
// The DSL gave a routine a durable watermark — `{{ routine.state.* }}` to read
// and a step's `state_write` binding to write — but nothing outside a run could
// SEE or CORRECT one. The failure mode is quiet and total: a routine writes a
// bad cursor once (a future timestamp, an id from the wrong source) and every
// later run then processes nothing, forever, while reporting success. Recovery
// meant a sqlite shell, which the ops contract forbids.
//
// These four routes are that recovery path, and the CLI (`crewship routine
// state`) is their front end.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// maxRoutineStateKeyLen bounds an operator-supplied state key. Values are
// bounded separately by the request-body limit in SetState.
const maxRoutineStateKeyLen = 128

// routineStateResponse is the wire shape for a state read. Buckets are always
// present (possibly empty) so a client can render "no state yet" without
// distinguishing null from [].
type routineStateResponse struct {
	Slug    string                 `json:"slug"`
	Buckets []pipeline.StateBucket `json:"buckets"`
}

// routineStateWriteBody is the PUT payload. ScheduleID selects the bucket;
// omitted means the shared manual/webhook bucket ("").
type routineStateWriteBody struct {
	Value      string `json:"value"`
	ScheduleID string `json:"schedule_id"`
}

// stateStore builds the operator-side store on demand. The executor gets its
// own via NewWiredExecutor; the handler only needs the same DB handle, so
// there's no extra constructor wiring to keep in sync.
func (h *PipelineHandler) stateStore() *pipeline.RoutineStateStore {
	if h == nil || h.db == nil {
		return nil
	}
	return pipeline.NewRoutineStateStore(h.db)
}

// resolvePipelineForState looks up the routine and writes the error response
// itself, returning ok=false when the caller should stop. Shared by all four
// handlers so the not-found/500 shape can't drift between them.
func (h *PipelineHandler) resolvePipelineForState(w http.ResponseWriter, r *http.Request) (*pipeline.Pipeline, bool) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return nil, false
	}
	if err != nil {
		replyInternalError(w, h.logger, "routine state: load routine", err)
		return nil, false
	}
	return p, true
}

// GetState GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state
//
// Without ?schedule_id= it returns EVERY bucket. That default is deliberate:
// the watermark lives in the bucket owned by whichever schedule wrote it, and
// "which schedule was that?" is precisely what an operator debugging a stalled
// routine does not know. Passing ?schedule_id= narrows to one bucket (an empty
// value is a legitimate selector — the manual/webhook bucket).
func (h *PipelineHandler) GetState(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolvePipelineForState(w, r)
	if !ok {
		return
	}
	store := h.stateStore()
	if store == nil {
		replyError(w, http.StatusServiceUnavailable, "routine state store not wired")
		return
	}

	buckets, err := store.Buckets(r.Context(), p.ID)
	if err != nil {
		replyInternalError(w, h.logger, "routine state: list buckets", err)
		return
	}
	// Distinguish "?schedule_id=" (select the manual bucket) from an absent
	// param (all buckets) — Query().Get can't, so ask whether the key exists.
	if r.URL.Query().Has("schedule_id") {
		want := r.URL.Query().Get("schedule_id")
		filtered := []pipeline.StateBucket{}
		for _, b := range buckets {
			if b.ScheduleID == want {
				filtered = append(filtered, b)
			}
		}
		buckets = filtered
	}
	writeJSON(w, http.StatusOK, routineStateResponse{Slug: p.Slug, Buckets: buckets})
}

// SetState PUT /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state/{key}
//
// Manage-tier: overwriting a watermark changes what every future UNATTENDED run
// of this routine does (skip everything / reprocess everything), so it sits at
// the same tier as disabling the routine rather than at run-tier.
func (h *PipelineHandler) SetState(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		replyError(w, http.StatusBadRequest, "key is required")
		return
	}
	// The key is half a primary key and is rendered into a template namespace
	// ({{ routine.state.<key> }}); an unbounded one is storage a caller
	// shouldn't get to define. 128 is far past any real cursor name.
	if len(key) > maxRoutineStateKeyLen {
		replyError(w, http.StatusBadRequest, "key exceeds 128 characters")
		return
	}
	p, ok := h.resolvePipelineForState(w, r)
	if !ok {
		return
	}
	store := h.stateStore()
	if store == nil {
		replyError(w, http.StatusServiceUnavailable, "routine state store not wired")
		return
	}

	var body routineStateWriteBody
	// Bounded read: the value is an operator-typed cursor, not a payload.
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	if err := store.Write(r.Context(), p.ID, body.ScheduleID, key, body.Value); err != nil {
		replyInternalError(w, h.logger, "routine state: write", err)
		return
	}
	h.logger.Info("routine state written by operator",
		"pipeline_id", p.ID, "slug", p.Slug, "schedule_id", body.ScheduleID, "key", key)
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":        p.Slug,
		"schedule_id": body.ScheduleID,
		"key":         key,
		"value":       body.Value,
	})
}

// DeleteStateKey DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state/{key}
//
// 404s a key that was never written rather than reporting a silent success —
// the usual reason a delete "doesn't work" is a mistyped key, and a 200 there
// would send the operator looking in the wrong place.
func (h *PipelineHandler) DeleteStateKey(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		replyError(w, http.StatusBadRequest, "key is required")
		return
	}
	p, ok := h.resolvePipelineForState(w, r)
	if !ok {
		return
	}
	store := h.stateStore()
	if store == nil {
		replyError(w, http.StatusServiceUnavailable, "routine state store not wired")
		return
	}

	scheduleID := r.URL.Query().Get("schedule_id")
	removed, err := store.Delete(r.Context(), p.ID, scheduleID, key)
	if err != nil {
		replyInternalError(w, h.logger, "routine state: delete key", err)
		return
	}
	if !removed {
		replyError(w, http.StatusNotFound, "no such key in this routine's state bucket")
		return
	}
	h.logger.Info("routine state key deleted by operator",
		"pipeline_id", p.ID, "slug", p.Slug, "schedule_id", scheduleID, "key", key)
	writeJSON(w, http.StatusOK, map[string]any{
		"slug": p.Slug, "schedule_id": scheduleID, "key": key, "deleted": true,
	})
}

// ClearState DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}/state
//
// Bucket-scoped by design — there is no "wipe every schedule" form. Each
// schedule's cursor is an independent watermark, and dropping all of them at
// once means every schedule reprocesses its whole backlog with no undo.
func (h *PipelineHandler) ClearState(w http.ResponseWriter, r *http.Request) {
	p, ok := h.resolvePipelineForState(w, r)
	if !ok {
		return
	}
	store := h.stateStore()
	if store == nil {
		replyError(w, http.StatusServiceUnavailable, "routine state store not wired")
		return
	}

	scheduleID := r.URL.Query().Get("schedule_id")
	n, err := store.Clear(r.Context(), p.ID, scheduleID)
	if err != nil {
		replyInternalError(w, h.logger, "routine state: clear bucket", err)
		return
	}
	h.logger.Info("routine state bucket cleared by operator",
		"pipeline_id", p.ID, "slug", p.Slug, "schedule_id", scheduleID, "removed", n)
	writeJSON(w, http.StatusOK, map[string]any{
		"slug": p.Slug, "schedule_id": scheduleID, "removed": n,
	})
}
