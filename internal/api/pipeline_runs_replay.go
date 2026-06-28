package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// marshalMetadata serializes a run's metadata object to JSON for
// storage. Empty/invalid → "{}" so the column never holds NULL.
func marshalMetadata(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// replayRun re-invokes the routine behind an existing run with the
// original inputs, stamping is_replay + replay_of. Shared by the
// single- and bulk-replay handlers. Returns the new run result.
func (h *PipelineHandler) replayRun(r *http.Request, workspaceID, runID string) (any, int, error) {
	if h.runStore == nil {
		return nil, http.StatusServiceUnavailable, errors.New("run store not wired")
	}
	orig, err := h.runStore.Get(r.Context(), runID)
	if errors.Is(err, pipeline.ErrRunNotFoundInStore) {
		return nil, http.StatusNotFound, errors.New("run not found")
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if orig.WorkspaceID != workspaceID {
		return nil, http.StatusNotFound, errors.New("run not found")
	}

	p, err := h.store.GetBySlug(r.Context(), workspaceID, orig.PipelineSlug)
	if errors.Is(err, pipeline.ErrNotFound) {
		return nil, http.StatusNotFound, errors.New("pipeline not found (deleted?)")
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	var inputs map[string]any
	if orig.InputsJSON != "" {
		_ = json.Unmarshal([]byte(orig.InputsJSON), &inputs)
	}
	// Carry the original run's tags so the replay groups with it.
	tags, _ := h.runStore.TagsFor(r.Context(), runID)

	exec := h.newExecutor()
	res, err := exec.Run(r.Context(), pipeline.RunInput{
		PipelineID:    p.ID,
		WorkspaceID:   workspaceID,
		Inputs:        inputs,
		Mode:          pipeline.ModeRun,
		TriggeredVia:  pipeline.TriggeredViaManual,
		TriggeredByID: runID,
		Tags:          tags,
		MetadataJSON:  orig.MetadataJSON,
		IsReplay:      true,
		ReplayOf:      runID,
	})
	if err != nil {
		if errors.Is(err, pipeline.ErrConcurrencyLimitReached) {
			return nil, http.StatusTooManyRequests, err
		}
		return nil, http.StatusInternalServerError, err
	}
	return res, http.StatusOK, nil
}

// ReplayRun re-runs a single prior run with its original inputs.
// POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay
func (h *PipelineHandler) ReplayRun(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	runID := r.PathValue("runId")
	if runID == "" {
		replyError(w, http.StatusBadRequest, "runId required")
		return
	}
	res, code, err := h.replayRun(r, workspaceID, runID)
	if err != nil {
		replyError(w, code, err.Error())
		return
	}
	writeJSON(w, code, res)
}

// bulkReplayBody selects which failed runs to replay: an explicit list
// of run ids, or every run sharing an error_fingerprint.
type bulkReplayBody struct {
	RunIDs      []string `json:"run_ids,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	// Limit caps how many runs a fingerprint selection expands to, so a
	// fingerprint with thousands of failures can't fan out unbounded.
	Limit int `json:"limit,omitempty"`
}

// BulkReplayRuns replays a set of failed runs after a fix shipped —
// either an explicit run_ids list or all runs under a fingerprint.
// POST /api/v1/workspaces/{workspaceId}/pipelines/runs/bulk_replay
func (h *PipelineHandler) BulkReplayRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run store not wired")
		return
	}
	var body bulkReplayBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	limit := body.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	ids := body.RunIDs
	if body.Fingerprint != "" {
		groups, err := h.runStore.FailureGroups(r.Context(), workspaceID, 200)
		if err != nil {
			replyError(w, http.StatusInternalServerError, "load failure groups")
			return
		}
		for _, g := range groups {
			if g.Fingerprint == body.Fingerprint {
				ids = append(ids, g.RunIDs...)
				break
			}
		}
	}
	if len(ids) == 0 {
		replyError(w, http.StatusBadRequest, "no runs selected (pass run_ids or fingerprint)")
		return
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}

	type replayOutcome struct {
		SourceRunID string `json:"source_run_id"`
		NewRunID    string `json:"new_run_id,omitempty"`
		Status      string `json:"status,omitempty"`
		Error       string `json:"error,omitempty"`
	}
	var (
		results  []replayOutcome
		replayed int
	)
	for _, id := range ids {
		res, _, err := h.replayRun(r, workspaceID, id)
		if err != nil {
			results = append(results, replayOutcome{SourceRunID: id, Error: err.Error()})
			continue
		}
		replayed++
		out := replayOutcome{SourceRunID: id}
		// res is *pipeline.RunResult; pull the run id/status reflectively
		// via JSON round-trip to avoid importing the concrete shape here.
		if b, err := json.Marshal(res); err == nil {
			var m struct {
				RunID  string `json:"run_id"`
				Status string `json:"status"`
			}
			_ = json.Unmarshal(b, &m)
			out.NewRunID, out.Status = m.RunID, m.Status
		}
		results = append(results, out)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requested": len(ids),
		"replayed":  replayed,
		"results":   results,
	})
}

// ListErrorGroups returns failed runs bucketed by error_fingerprint for
// the errors view + bulk replay.
// GET /api/v1/workspaces/{workspaceId}/pipelines/runs/errors?limit=50
func (h *PipelineHandler) ListErrorGroups(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if h.runStore == nil {
		replyError(w, http.StatusServiceUnavailable, "run store not wired")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	groups, err := h.runStore.FailureGroups(r.Context(), workspaceID, limit)
	if err != nil {
		h.logger.Error("list error groups", "error", err)
		replyError(w, http.StatusInternalServerError, "load failure groups")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}
