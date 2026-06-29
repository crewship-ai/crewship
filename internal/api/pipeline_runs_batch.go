package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// maxBatchItems bounds a single batch trigger so one request can't fan
// out unbounded runs (each run executes synchronously).
const maxBatchItems = 50

// batchItem is one run in a batch: its own inputs (+ optional per-run
// tags/metadata). Tags/metadata default to the batch-level values.
type batchItem struct {
	Inputs   map[string]any `json:"inputs"`
	Tags     []string       `json:"tags,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type batchRunBody struct {
	Items []batchItem `json:"items"`
	// Tags applied to every run in the batch (in addition to per-item
	// tags and the synthetic batch:<id> tag).
	Tags         []string `json:"tags,omitempty"`
	TierOverride string   `json:"tier_override,omitempty"`
}

// RunBatch triggers N runs of one routine from an array of input sets
// (trigger.dev batchTrigger parity). Every run is tagged batch:<id> so
// the set is retrievable via `routine runs --tag batch:<id>`. Runs
// execute sequentially (the executor is synchronous); the response
// returns each run's id + status.
// POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run_batch
func (h *PipelineHandler) RunBatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	if h.runner == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline runner not wired")
		return
	}
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	// Same governance status gate as Run — a proposed/disabled routine can't
	// be batch-triggered either.
	if h.gateRoutineStatus(w, p) {
		return
	}

	var body batchRunBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Items) == 0 {
		replyError(w, http.StatusBadRequest, "batch requires at least one item")
		return
	}
	if len(body.Items) > maxBatchItems {
		replyError(w, http.StatusBadRequest, fmt.Sprintf("batch too large (%d > %d max)", len(body.Items), maxBatchItems))
		return
	}

	batchID := "batch_" + generateCUID()
	batchTag := "batch:" + batchID
	tierOverride := pipeline.Complexity(body.TierOverride)

	type itemResult struct {
		Index  int    `json:"index"`
		RunID  string `json:"run_id,omitempty"`
		Status string `json:"status,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	results := make([]itemResult, 0, len(body.Items))
	for i, item := range body.Items {
		tags := append([]string{batchTag}, body.Tags...)
		tags = append(tags, item.Tags...)
		exec := h.newExecutor()
		res, err := exec.Run(r.Context(), pipeline.RunInput{
			PipelineID:   p.ID,
			WorkspaceID:  workspaceID,
			Inputs:       item.Inputs,
			Mode:         pipeline.ModeRun,
			TierOverride: tierOverride,
			TriggeredVia: pipeline.TriggeredViaManual,
			Tags:         tags,
			MetadataJSON: marshalMetadata(item.Metadata),
		})
		if err != nil {
			results = append(results, itemResult{Index: i, Error: err.Error()})
			continue
		}
		out := itemResult{Index: i}
		if b, mErr := json.Marshal(res); mErr == nil {
			var m struct {
				RunID  string `json:"run_id"`
				Status string `json:"status"`
			}
			_ = json.Unmarshal(b, &m)
			out.RunID, out.Status = m.RunID, m.Status
		}
		results = append(results, out)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"batch_id": batchID,
		"tag":      batchTag,
		"count":    len(results),
		"results":  results,
	})
}
