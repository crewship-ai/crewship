package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// maxExecBodyBytes caps the JSON request body on the pipeline exec
// surface (run / dry_run / test / approve). Run inputs and inline
// definitions can dwarf a UI-preference blob, so this sits well above
// user_preferences.go's 16 KB — but still bounds the decoder so a single
// oversized POST can't pin memory. MaxBytesReader trips past the cap and
// Decode surfaces the error as a 400.
const maxExecBodyBytes = 1 << 20 // 1 MiB

// runRequestBody is the shared shape for /run + /dry_run.
//
// TierOverride is the eval-suite knob that replaces every agent_run
// step's complexity for the duration of one run. Accepted values
// match pipeline.Complexity (trivial | fast | moderate | smart);
// any other value is silently ignored (treat as no override) so a
// future tier name added to the executor doesn't break old clients.
type runRequestBody struct {
	Inputs       map[string]any `json:"inputs"`
	TierOverride string         `json:"tier_override,omitempty"`
	// TriggeredVia + TriggeredByID let the caller (UI button, issue
	// detail panel, etc.) attribute the run for the dashboards. Server
	// validates against the closed enum so a malicious / typo'd value
	// can't show up in the runs list as a forged source. Defaults to
	// "manual" when empty.
	TriggeredVia  string `json:"triggered_via,omitempty"`
	TriggeredByID string `json:"triggered_by_id,omitempty"`
}

// Run invokes a saved pipeline by slug.
// POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run
//
// Body: { "inputs": { ... } }  (all fields optional; defaults applied
// per the pipeline's input spec)
//
// Returns: full RunResult with status, output, step_outputs,
// duration_ms, cost_usd. For a streaming run dashboard, subscribe to
// the workspace WebSocket channel and watch for pipeline.* journal
// entries — the run id in the response payload joins them.
func (h *PipelineHandler) Run(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	if h.runner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "pipeline runner not wired",
			"hint":  "the orchestrator hasn't booted yet, or this build was assembled without the runner adapter",
		})
		return
	}

	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline run: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}

	var body runRequestBody
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
			replyError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	// invoking_crew_id / invoking_agent_id come from a future
	// header (X-Crewship-Invoking-Crew, X-Crewship-Invoking-Agent)
	// that the sidecar will inject when an in-container agent
	// triggers the run. UI-driven runs leave both empty — that's
	// fine; the journal entry just records "user-driven" rather
	// than "Crew B → Crew A".
	invokingCrew := r.Header.Get("X-Crewship-Invoking-Crew")
	invokingAgent := r.Header.Get("X-Crewship-Invoking-Agent")

	// Idempotency-Key header dedupes webhook redeliveries: a second
	// request with the same key (within 24h) returns the original
	// run id with status=DEDUPED instead of executing twice. Falls
	// through silently if the executor's idempotency store isn't
	// wired (tests, dev without DB).
	idempotencyKey := r.Header.Get("Idempotency-Key")

	tierOverride := pipeline.Complexity(body.TierOverride)
	switch tierOverride {
	case "", pipeline.ComplexityTrivial, pipeline.ComplexityFast, pipeline.ComplexityModerate, pipeline.ComplexitySmart:
		// accepted (empty = no override)
	default:
		// Unknown tier: drop the override silently. A future tier
		// name added server-side will then ignore old clients
		// gracefully rather than 400-ing them.
		tierOverride = ""
	}

	// Validate triggered_via against the closed enum so the runs list
	// dashboard can trust the value without sanitizing again. Anything
	// outside the enum falls back to "manual" — same forgive-and-carry-on
	// semantics as TierOverride above.
	triggeredVia := pipeline.TriggeredVia(body.TriggeredVia)
	switch triggeredVia {
	case pipeline.TriggeredViaManual,
		pipeline.TriggeredViaSchedule,
		pipeline.TriggeredViaWebhook,
		pipeline.TriggeredViaCallPipeline,
		pipeline.TriggeredViaIssue:
		// accepted
	default:
		triggeredVia = pipeline.TriggeredViaManual
	}

	exec := h.newExecutor()
	res, err := exec.Run(r.Context(), pipeline.RunInput{
		PipelineID:      p.ID,
		WorkspaceID:     workspaceID,
		InvokingCrewID:  invokingCrew,
		InvokingAgentID: invokingAgent,
		Inputs:          body.Inputs,
		Mode:            pipeline.ModeRun,
		IdempotencyKey:  idempotencyKey,
		TierOverride:    tierOverride,
		TriggeredVia:    triggeredVia,
		TriggeredByID:   body.TriggeredByID,
	})
	if err != nil {
		// Concurrency rejection is a normal 429, not an internal
		// error. Map before the catch-all.
		if errors.Is(err, pipeline.ErrConcurrencyLimitReached) {
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{
				"error":  "concurrency limit reached for this pipeline",
				"reason": "another run with the same concurrency_key is already in flight",
			})
			return
		}
		h.logger.Error("pipeline run: exec", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "Failed to start pipeline run")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// DryRun returns the structured WouldExecute report for a saved
// pipeline against the supplied inputs. No agent invocations,
// no journal entries beyond a single audit row.
// POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/dry_run
func (h *PipelineHandler) DryRun(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline dry_run: load", "error", err)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	var body runRequestBody
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
			replyError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	exec := h.newExecutor()
	res, err := exec.Run(r.Context(), pipeline.RunInput{
		PipelineID:  p.ID,
		WorkspaceID: workspaceID,
		Inputs:      body.Inputs,
		Mode:        pipeline.ModeDryRun,
	})
	if err != nil {
		h.logger.Error("pipeline dry_run: exec", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to dry-run pipeline")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// TestRun executes a draft DSL (NOT yet saved) against the
// execution tier so the author can confirm the pipeline runs
// before calling save. Used by the save-gate flow: agent posts a
// draft, gets a passed/failed report; on pass the author's session
// "owns" a fresh test_run timestamp it can include in the save.
//
// POST /api/v1/workspaces/{workspaceId}/pipelines/test_run
//
// Body: { "definition": { ...DSL... }, "sample_inputs": { ... } }
//
// Returns: RunResult with status COMPLETED | FAILED. The save
// endpoint will check the same DSL hash + a recent timestamp
// before persisting.
func (h *PipelineHandler) TestRun(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if h.runner == nil {
		replyError(w, http.StatusServiceUnavailable, "pipeline runner not wired")
		return
	}

	var body struct {
		Definition   json.RawMessage `json:"definition"`
		AuthorCrewID string          `json:"author_crew_id"`
		SampleInputs map[string]any  `json:"sample_inputs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Definition) == 0 {
		replyError(w, http.StatusBadRequest, "definition required")
		return
	}
	if body.AuthorCrewID == "" {
		replyError(w, http.StatusBadRequest, "author_crew_id required")
		return
	}

	dsl, err := pipeline.Parse(body.Definition)
	if err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// Cross-reference validation on save uses agent_slug ⊆ author
	// crew membership. For the test_run we accept any slug — agent
	// slugs are validated again at save time. This keeps the
	// authoring loop fast (an iteration that fails because of a
	// typo gets a quick error from the runner, not a strict
	// schema check).
	if err := pipeline.Validate(dsl, nil, nil); err != nil {
		replyError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	exec := h.newExecutor()
	res, err := exec.RunDefinition(r.Context(), dsl, pipeline.RunInput{
		WorkspaceID:  workspaceID,
		AuthorCrewID: body.AuthorCrewID,
		Inputs:       body.SampleInputs,
		Mode:         pipeline.ModeTestRun,
	})
	// Mint an HMAC save_token bound to (workspace, definition_hash,
	// user) when the test_run passed AND a signing secret is wired.
	// The token is the trustworthy proof that THIS user ran this DSL
	// successfully — Save can verify it without trusting the body's
	// last_test_run_at claim.
	var saveToken string
	if err == nil && res != nil && res.Status == "COMPLETED" && len(h.saveTokenSecret) > 0 {
		user := UserFromContext(r.Context())
		userID := ""
		if user != nil {
			userID = user.ID
		}
		defHash := definitionHashHex(body.Definition)
		saveToken = signSaveToken(h.saveTokenSecret, workspaceID, defHash, userID, time.Now())
	}
	if err != nil {
		h.logger.Error("pipeline test_run: exec", "error", err)
		replyError(w, http.StatusInternalServerError, "Failed to test-run pipeline")
		return
	}
	// Wrap the RunResult with save_token. Embed the result's fields
	// at the top level so existing clients (CLI watchers, UI dialog)
	// see no shape change; new clients can opt in to the save_token
	// flow by reading the extra field.
	type testRunResponse struct {
		*pipeline.RunResult
		SaveToken string `json:"save_token,omitempty"`
	}
	writeJSON(w, http.StatusOK, testRunResponse{RunResult: res, SaveToken: saveToken})
}

// ListRunRecords returns runs from the pipeline_runs table directly
// (column-typed, B-tree scan). Faster than ListRuns because it skips
// the LIKE-pattern + json_extract path on journal_entries; ideal for
// the active-runs dashboard and run-history list views that don't
// need per-step event detail.
//
// GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run-records
//
// Returns 503 when the runStore is not wired (legacy deployment with
// only journal-backed runs); UI clients should fall back to ListRuns
// in that case.
func (h *PipelineHandler) ListRunRecords(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	if h.runStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "pipeline_runs store not wired; fall back to /runs (journal-backed)",
			"hint":   "this deployment predates migration v83 or runStore is unset in cmd_start.go",
			"legacy": "/runs",
		})
		return
	}
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline list run-records: load", "error", err)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parseSmallInt(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	statusFilter := pipeline.RunStatus(r.URL.Query().Get("status"))
	records, err := h.runStore.ListByPipeline(r.Context(), p.ID, statusFilter, limit)
	if err != nil {
		h.logger.Error("pipeline list run-records: query", "error", err)
		replyError(w, http.StatusInternalServerError, "list run records")
		return
	}
	// Stable wire shape — explicit DTO so internal renames don't
	// silently break the API contract.
	type runRecordDTO struct {
		ID               string  `json:"id"`
		PipelineID       string  `json:"pipeline_id"`
		PipelineSlug     string  `json:"pipeline_slug"`
		Status           string  `json:"status"`
		Mode             string  `json:"mode"`
		StartedAt        string  `json:"started_at"`
		EndedAt          string  `json:"ended_at,omitempty"`
		CurrentStepID    string  `json:"current_step_id,omitempty"`
		Output           string  `json:"output,omitempty"`
		CostUSD          float64 `json:"cost_usd"`
		DurationMs       int64   `json:"duration_ms"`
		ErrorMessage     string  `json:"error_message,omitempty"`
		FailedAtStep     string  `json:"failed_at_step,omitempty"`
		ErrorFingerprint string  `json:"error_fingerprint,omitempty"`
		TriggeredVia     string  `json:"triggered_via"`
		TriggeredByID    string  `json:"triggered_by_id,omitempty"`
		IdempotencyKey   string  `json:"idempotency_key,omitempty"`
	}
	out := make([]runRecordDTO, 0, len(records))
	for _, rec := range records {
		dto := runRecordDTO{
			ID:            rec.ID,
			PipelineID:    rec.PipelineID,
			PipelineSlug:  rec.PipelineSlug,
			Status:        string(rec.Status),
			Mode:          string(rec.Mode),
			StartedAt:     rec.StartedAt.Format(time.RFC3339Nano),
			CurrentStepID: rec.CurrentStepID,
			Output:        rec.Output,
			CostUSD:       rec.CostUSD,
			DurationMs:    rec.DurationMs,
			// Sanitize: error_message comes verbatim from executor /
			// runner / DB driver — could carry stack traces, file
			// paths, half-rendered prompts, secrets the validation
			// gate didn't catch. Truncate hard at 200 chars and
			// strip anything past the first newline so multi-line
			// stack traces don't leak through the dashboard. Full
			// error stays in journal_entries (audit-of-record).
			ErrorMessage:     truncateErrorForList(rec.ErrorMessage),
			FailedAtStep:     rec.FailedAtStep,
			ErrorFingerprint: rec.ErrorFingerprint,
			TriggeredVia:     string(rec.TriggeredVia),
			TriggeredByID:    rec.TriggeredByID,
			IdempotencyKey:   rec.IdempotencyKey,
		}
		if rec.EndedAt != nil && !rec.EndedAt.IsZero() {
			dto.EndedAt = rec.EndedAt.Format(time.RFC3339Nano)
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// ListRuns returns the journal entries for the named pipeline,
// filtered server-side to pipeline.* entry types so the response
// is purpose-built for a runs table UI without leaking unrelated
// activity.
// GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/runs
func (h *PipelineHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "pipeline not found")
		return
	}
	if err != nil {
		h.logger.Error("pipeline list runs: load", "error", err)
		replyError(w, http.StatusInternalServerError, "load pipeline")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		// Cheap parse — out-of-range falls back to the default.
		if n, err := parseSmallInt(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	// include_steps=1 widens the filter to also return
	// pipeline.step.* entries so the UI can render a waterfall
	// timeline for each run. Default off to keep the list-page
	// payload small (a 5-step pipeline run produces 11 entries:
	// 1 run.started + 5 step.started + 5 step.completed +
	// 1 run.completed; multiply by 50 runs and the response
	// balloons). Detail panel passes ?include_steps=1.
	includeSteps := r.URL.Query().Get("include_steps") == "1"

	// We index pipeline runs purely through journal_entries.
	// json_extract is supported by modernc.org/sqlite + every
	// recent mainline SQLite (>= 3.38), so we use it inline for
	// the pipeline_id filter rather than carrying a "fast path
	// vs fallback" branch (the previous version had a 7-column
	// fast path the scanner couldn't decode — dead code per
	// CodeRabbit).
	entryFilter := "pipeline.run.%"
	if includeSteps {
		entryFilter = "pipeline.%"
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, ts, entry_type, severity, summary, payload
FROM journal_entries
WHERE workspace_id = ?
  AND entry_type LIKE ?
  AND json_extract(payload, '$.pipeline_id') = ?
ORDER BY ts DESC
LIMIT ?`, workspaceID, entryFilter, p.ID, limit)
	if err != nil {
		h.logger.Error("pipeline list runs: query", "error", err)
		replyError(w, http.StatusInternalServerError, "list runs")
		return
	}
	defer rows.Close()

	type runEntry struct {
		ID         string          `json:"id"`
		Timestamp  string          `json:"ts"`
		EntryType  string          `json:"entry_type"`
		Severity   string          `json:"severity"`
		Summary    string          `json:"summary"`
		PipelineID string          `json:"pipeline_id"`
		RunID      string          `json:"run_id,omitempty"`
		Payload    json.RawMessage `json:"payload"`
	}
	out := make([]runEntry, 0, limit)
	for rows.Next() {
		var (
			e          runEntry
			payloadRaw string
		)
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.EntryType, &e.Severity, &e.Summary, &payloadRaw); err != nil {
			h.logger.Warn("pipeline list runs: scan", "error", err)
			continue
		}
		// SQL already filtered by pipeline_id; we still parse the
		// payload to surface run_id (and confirm pipeline_id) on
		// the wire. JSON parse failures are non-fatal — surface
		// the row anyway so a malformed payload doesn't hide a
		// real run from the dashboard.
		var meta map[string]any
		if err := json.Unmarshal([]byte(payloadRaw), &meta); err == nil {
			if pid, ok := meta["pipeline_id"].(string); ok {
				e.PipelineID = pid
			}
			if rid, ok := meta["run_id"].(string); ok {
				e.RunID = rid
			}
		}
		e.Payload = json.RawMessage(payloadRaw)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		h.logger.Warn("pipeline list runs: rows iteration", "error", err)
	}
	writeJSON(w, http.StatusOK, out)
}

// ApproveWaitpoint completes a pending wait-step approval. POST
// /api/v1/workspaces/{ws}/pipelines/waitpoints/{token}/approve
// Body: { "approved": true|false, "comment": "..." }
//
// Reaches into the wired WaitpointStore (production: SQLWaitpointStore
// from internal/pipeline). The corresponding pipeline run goroutine
// is parked on WaitFor(token); this call wakes it.
func (h *PipelineHandler) ApproveWaitpoint(w http.ResponseWriter, r *http.Request) {
	if h.waitpoints == nil {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store not wired")
		return
	}
	token := r.PathValue("token")
	if token == "" {
		replyError(w, http.StatusBadRequest, "token required")
		return
	}
	var body struct {
		Approved bool   `json:"approved"`
		Comment  string `json:"comment"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExecBodyBytes)).Decode(&body); err != nil {
			replyError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	// We accept either the SQLWaitpointStore concrete type or any
	// other implementation that satisfies the interface. The
	// CompleteApproval call is a method on the SQL store, but the
	// interface only exposes WaitFor + CreateApproval — so we type-
	// assert here. Production wiring always uses the SQL store.
	type approver interface {
		CompleteApproval(ctx context.Context, token string, approved bool, deciderUserID, payload string) error
	}
	wp, ok := h.waitpoints.(approver)
	if !ok {
		replyError(w, http.StatusServiceUnavailable, "waitpoint store does not support completion")
		return
	}
	// Decider identity from the JWT user context — same path the
	// rest of the routine handlers use. Empty string when the
	// request didn't go through authedMw (test paths without auth);
	// the waitpoint row's decided_by_user_id ends up NULL in that
	// case, which is fine for downstream audit queries.
	deciderID := ""
	if user := UserFromContext(r.Context()); user != nil {
		deciderID = user.ID
	}
	payload := body.Comment
	if err := wp.CompleteApproval(r.Context(), token, body.Approved, deciderID, payload); err != nil {
		// pipeline.ErrAlreadyDecided → 409
		if err.Error() == "waitpoint: already decided or expired" {
			replyError(w, http.StatusConflict, err.Error())
			return
		}
		h.logger.Error("waitpoint complete", "error", err, "token", tokenFingerprint(token))
		replyError(w, http.StatusInternalServerError, "Failed to complete waitpoint")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": body.Approved})
}

// ListPendingWaitpoints returns the workspace's pending approval
// waitpoints so the inbox UI can render approval cards. GET
// /api/v1/workspaces/{ws}/pipelines/waitpoints
func (h *PipelineHandler) ListPendingWaitpoints(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.db.QueryContext(r.Context(), `
SELECT token, pipeline_run_id, step_id, kind, COALESCE(prompt, ''), COALESCE(invoking_crew_id, ''),
       timeout_at, created_at
FROM pipeline_waitpoints
WHERE workspace_id = ? AND status = 'pending'
ORDER BY created_at DESC
LIMIT 200`, workspaceID)
	if err != nil {
		h.logger.Error("waitpoints list", "error", err)
		replyError(w, http.StatusInternalServerError, "list waitpoints")
		return
	}
	defer rows.Close()
	type wpRow struct {
		Token          string `json:"token"`
		PipelineRunID  string `json:"pipeline_run_id"`
		StepID         string `json:"step_id"`
		Kind           string `json:"kind"`
		Prompt         string `json:"prompt"`
		InvokingCrewID string `json:"invoking_crew_id,omitempty"`
		TimeoutAt      string `json:"timeout_at"`
		CreatedAt      string `json:"created_at"`
	}
	out := make([]wpRow, 0, 50)
	for rows.Next() {
		var r wpRow
		if err := rows.Scan(&r.Token, &r.PipelineRunID, &r.StepID, &r.Kind, &r.Prompt, &r.InvokingCrewID, &r.TimeoutAt, &r.CreatedAt); err == nil {
			out = append(out, r)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
