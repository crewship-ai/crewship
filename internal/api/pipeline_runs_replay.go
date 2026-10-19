package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/runauthority"
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

// replayRequestBody carries optional replay-time overrides. Decoded
// only by the single-run ReplayRun handler — bulk-replay stays
// unpinned (it exists to re-run a fingerprint group after a fix ships
// to HEAD, not to backtest a candidate version).
type replayRequestBody struct {
	// PinnedVersion, when set, replays against that immutable
	// pipeline_versions row's definition instead of HEAD. This is the
	// primitive `crewship routine backtest` composes on: N replays of
	// recent captured-input runs, all pinned to the same candidate
	// version, diffed against the originals. These are real ModeRun
	// executions with external effects and costs; only HEAD/live stays
	// unchanged (see RunInput.PinnedVersion in internal/pipeline/executor.go).
	PinnedVersion *int            `json:"pinned_version,omitempty"`
	Inputs        json.RawMessage `json:"inputs,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	Tags          json.RawMessage `json:"tags,omitempty"`
}

// Raw fields distinguish inheritance (omitted) from clearing ({} or []). Null
// is refused so clients cannot accidentally turn a missing value into a reset.
func (b replayRequestBody) overrides() (map[string]any, map[string]any, []string, error) {
	objects := make([]map[string]any, 2)
	for i, field := range []struct {
		name string
		raw  json.RawMessage
	}{{"inputs", b.Inputs}, {"metadata", b.Metadata}} {
		if len(field.raw) == 0 {
			continue
		}
		if err := json.Unmarshal(field.raw, &objects[i]); err != nil || objects[i] == nil {
			return nil, nil, nil, fmt.Errorf("%s must be a JSON object; omit it to inherit", field.name)
		}
	}
	if objects[1] != nil && len(marshalMetadata(objects[1])) > pipeline.MaxRunMetadataBytes {
		return nil, nil, nil, fmt.Errorf("metadata exceeds %d bytes", pipeline.MaxRunMetadataBytes)
	}
	var tags []string
	if len(b.Tags) != 0 {
		var values []any
		if err := json.Unmarshal(b.Tags, &values); err != nil || values == nil {
			return nil, nil, nil, errors.New("tags must be an array of strings; omit it to inherit")
		}
		if len(values) > pipeline.MaxRunTags {
			return nil, nil, nil, fmt.Errorf("at most %d tags are allowed", pipeline.MaxRunTags)
		}
		tags = make([]string, 0, len(values))
		for _, value := range values {
			tag, ok := value.(string)
			if !ok {
				return nil, nil, nil, errors.New("tags must be an array of strings")
			}
			tags = append(tags, tag)
		}
	}
	return objects[0], objects[1], tags, nil
}

// replayRun re-invokes the routine behind an existing run with the
// original inputs, stamping is_replay + replay_of. Shared by the
// single- and bulk-replay handlers. Returns the new run result.
// An omitted pin replays against HEAD; bulk replay inherits all captured fields.
func (h *PipelineHandler) replayRun(r *http.Request, workspaceID, runID string, options replayRequestBody) (any, int, error) {
	user := UserFromContext(r.Context())
	if user == nil || user.ID == "" {
		return nil, http.StatusUnauthorized, errors.New("replay requires an authenticated user")
	}
	if h.runStore == nil {
		return nil, http.StatusServiceUnavailable, errors.New("run store not wired")
	}
	inputOverride, metadataOverride, tagOverride, err := options.overrides()
	if err != nil {
		return nil, http.StatusBadRequest, err
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
	if inputOverride != nil {
		inputs = inputOverride
	} else if orig.InputsJSON != "" {
		if err := json.Unmarshal([]byte(orig.InputsJSON), &inputs); err != nil {
			return nil, http.StatusConflict, errors.New("historical inputs are unreadable; replay was not started")
		}
	}
	// Validate changed inputs against the selected immutable definition before
	// any run or external effect. The executor repeats validation at dispatch.
	if inputOverride != nil {
		definition := p.DefinitionJSON
		if options.PinnedVersion != nil {
			version, err := h.store.GetVersion(r.Context(), p.ID, *options.PinnedVersion)
			if errors.Is(err, pipeline.ErrNotFound) || (err == nil && version == nil) {
				return nil, http.StatusConflict, pipeline.ErrPinnedVersionNotFound
			}
			if err != nil {
				return nil, http.StatusInternalServerError, err
			}
			definition = version.DefinitionJSON
		}
		dsl, err := pipeline.Parse([]byte(definition))
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if err := pipeline.ValidateFormInputs(dsl, inputs); err != nil {
			return nil, http.StatusBadRequest, err
		}
	}
	// Carry the original run's tags so the replay groups with it.
	tags := tagOverride
	if tags == nil {
		tags, err = h.runStore.TagsFor(r.Context(), runID)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
	}
	metadataJSON := orig.MetadataJSON
	if metadataOverride != nil {
		metadataJSON = marshalMetadata(metadataOverride)
	}

	// Preserve both the historical source and the trusted caller's ceiling.
	// A single origin column cannot represent two distinct private sources.
	originID := orig.ChatOriginID
	callerOrigin := runauthority.OriginIDFromContext(r.Context())
	if originID != "" && callerOrigin != "" && originID != callerOrigin {
		return nil, http.StatusConflict, errors.New("replay has incompatible private origins")
	}
	if originID == "" {
		originID = callerOrigin
	}
	triggeredVia := pipeline.TriggeredViaManual
	if orig.TriggeredVia == pipeline.TriggeredViaIssue {
		triggeredVia = pipeline.TriggeredViaIssue
	}

	exec := h.newExecutor()
	res, err := exec.Run(r.Context(), pipeline.RunInput{
		PipelineID:     p.ID,
		WorkspaceID:    workspaceID,
		InvokingUserID: user.ID,
		// A replay is new work authorized for the operator starting it,
		// never a continuation under the original run's principal.
		InvocationAuthority: pipeline.RoutineBatchAuthority,
		Inputs:              inputs,
		Mode:                pipeline.ModeRun,
		TriggeredVia:        triggeredVia,
		ChatOriginID:        originID,
		TriggeredByID:       runID,
		Tags:                tags,
		MetadataJSON:        metadataJSON,
		IsReplay:            true,
		ReplayOf:            runID,
		PinnedVersion:       options.PinnedVersion,
	})
	if err != nil {
		if errors.Is(err, pipeline.ErrInvocationAuthorityRevoked) || errors.Is(err, pipeline.ErrInvokingUserNotMember) {
			return nil, http.StatusForbidden, errors.New("replay permission no longer valid")
		}
		if errors.Is(err, pipeline.ErrConcurrencyLimitReached) {
			return nil, http.StatusTooManyRequests, err
		}
		// The caller pinned a version that no longer exists — a request
		// problem, not a server fault. 409 with the executor's legible
		// error (names routine + version), same mapping the schedule
		// force-fire and webhook fire paths use for the same error.
		// Never fall back to executing HEAD silently: a backtest that
		// silently compared HEAD against itself would misreport as a
		// clean pass.
		if errors.Is(err, pipeline.ErrPinnedVersionNotFound) {
			return nil, http.StatusConflict, err
		}
		return nil, http.StatusInternalServerError, err
	}
	return res, http.StatusOK, nil
}

// ReplayRun re-runs a single prior run with its original inputs.
// POST /api/v1/workspaces/{workspaceId}/pipelines/runs/{runId}/replay
func (h *PipelineHandler) ReplayRun(w http.ResponseWriter, r *http.Request) {
	// Replaying spawns a fresh run — a control-plane mutation, MANAGER+ (create).
	if !requireRole(w, r, "create") {
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	runID := r.PathValue("runId")
	if runID == "" {
		replyError(w, http.StatusBadRequest, "runId required")
		return
	}
	var body replayRequestBody
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes))
		if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			replyError(w, http.StatusBadRequest, "invalid request body")
			return
		} else if err == nil {
			var extra any
			if decoder.Decode(&extra) != io.EOF {
				replyError(w, http.StatusBadRequest, "request body must contain one JSON object")
				return
			}
		}
	}
	if body.PinnedVersion != nil && *body.PinnedVersion < 1 {
		replyError(w, http.StatusBadRequest, "pinned_version must be a positive archive version")
		return
	}
	res, code, err := h.replayRun(r, workspaceID, runID, body)
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
	if !requireRole(w, r, "create") {
		return
	}
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
		res, _, err := h.replayRun(r, workspaceID, id, replayRequestBody{})
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
