package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// SignalWorkspace delivers a payload to EVERY run in the workspace parked on
// a wait:event step for the given event_type, and un-parks each one.
//
// The per-run sibling (SignalRun) needs a run id, which only works for a
// caller that was handed one. An event source inside Crewship — a mission
// changing status, an issue being commented on — knows the workspace and the
// event and nothing about who is listening; making it discover the run set
// would put the same lookup in every producer, each free to get the workspace
// fence subtly wrong. This is that lookup, once, next to the claim it depends
// on.
//
// Auth mirrors SignalRun exactly (MANAGER+ via "update"): delivering a signal
// resumes a parked run, which is a control-plane act on someone's workflow
// regardless of how the run was addressed. The workspace fence is the context
// workspace, applied inside the store's claim — there is no run id to
// cross-check here, so the query itself has to be the boundary.
//
// POST /api/v1/workspaces/{workspaceId}/signals
func (h *PipelineHandler) SignalWorkspace(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "update") {
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	var body struct {
		EventType string `json:"event_type"`
		Payload   string `json:"payload"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.EventType == "" {
		replyError(w, http.StatusBadRequest, "event_type required")
		return
	}
	// No DB, no topic: the in-memory SignalRegistry is keyed by run id and
	// therefore cannot answer "who is waiting on this event", so there is
	// no degraded mode to fall back to here (unlike SignalRun, which can
	// still wake a live goroutine it was pointed at).
	if h.db == nil {
		replyError(w, http.StatusServiceUnavailable, "signal wait store not wired")
		return
	}

	runIDs, err := pipeline.NewSQLSignalWaitStore(h.db).
		DeliverTopic(r.Context(), workspaceID, body.EventType, body.Payload)
	truncated := errors.Is(err, pipeline.ErrTopicFanoutTruncated)
	if err != nil && !truncated {
		h.logger.Error("signal workspace: durable deliver",
			"error", err, "workspace_id", workspaceID, "event_type", body.EventType)
		replyError(w, http.StatusInternalServerError, "failed to record signal delivery")
		return
	}

	// Resume every run we claimed. Best-effort in-memory Signal first for
	// the runs that DO have a live goroutine (a nested, non-top-level wait
	// blocks in-process rather than parking), then ResumeAfterSignal for
	// the parked majority — the same order and the same reasoning as
	// SignalRun, which no-ops harmlessly on whichever of the two the run
	// did not need.
	for _, runID := range runIDs {
		if h.signals != nil {
			h.signals.Signal(runID, body.EventType, body.Payload)
		}
		h.newExecutor().ResumeAfterSignal(runID, h.logger)
	}

	// Zero deliveries is 200, not 404. An internal producer emits an event
	// whether or not a routine happens to be parked on it; answering 404
	// would make the ordinary case look like a failure and teach callers
	// to ignore the status code. "Nobody was listening" is reported in the
	// body as delivered: 0.
	resp := map[string]any{
		"ok":        true,
		"delivered": len(runIDs),
		"run_ids":   runIDs,
	}
	if truncated {
		// Partial success: the runs above WERE delivered and resumed;
		// the rest are still pending and a second call claims them.
		resp["truncated"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}
