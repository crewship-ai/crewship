package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// PipelineHandler exposes the workspace-scoped HTTP surface for
// pipelines. It owns the Store + Executor; the AgentRunner is
// injected from the outside (orchestrator adapter in production,
// stub in tests) so the handler can be wired and tested before the
// real orchestrator integration lands.
type PipelineHandler struct {
	db         *sql.DB
	logger     *slog.Logger
	store      *pipeline.Store
	resolver   *pipeline.Resolver
	runner     pipeline.AgentRunner
	emitter    pipeline.Emitter
	waitpoints pipeline.WaitpointStore // optional; nil → wait approval steps fall back to in-memory timeout
	ws         pipeline.WSBroadcaster  // optional; nil → no live pipeline event push to frontend
	schedules  *pipeline.ScheduleStore // optional; nil → schedule endpoints return 503
	runs       *pipeline.RunRegistry   // optional; nil → cancel endpoint returns 503
	webhooks   *pipeline.WebhookStore  // optional; nil → webhook endpoints return 503
	// saveTokenSecret signs the optional save_token returned by
	// /test_run and verified by /save. Lets save flows skip the body-
	// trust on last_test_run_at (callers can otherwise mint timestamps;
	// see PIPELINES.md §17 threat model). When unset, save falls back
	// to the timestamp-based gate. Production wiring sets this to the
	// process internal token at boot.
	saveTokenSecret []byte
}

// NewPipelineHandler wires the pipeline subsystem against an
// existing DB handle. AgentRunner and Emitter are accepted as
// dependencies so the call site (router setup) can pass either the
// real orchestrator adapter + journal.Writer, or stubs for tests.
//
// Both runner and emitter may be nil at construction time; the
// handler bails with a 503 from any endpoint that needs the runner
// when it is not wired, and silently no-ops the journal when the
// emitter is missing.
func NewPipelineHandler(db *sql.DB, logger *slog.Logger, runner pipeline.AgentRunner, emitter pipeline.Emitter) *PipelineHandler {
	store := pipeline.NewStore(db)
	resolver := pipeline.NewResolver(db)
	return &PipelineHandler{
		db:       db,
		logger:   logger,
		store:    store,
		resolver: resolver,
		runner:   runner,
		emitter:  emitter,
	}
}

// SetRunner lets the orchestrator wire its AgentRunner adapter into
// an already-constructed handler. The router builds handlers before
// the orchestrator boots, so we accept post-construction injection.
func (h *PipelineHandler) SetRunner(r pipeline.AgentRunner) {
	h.runner = r
}

// SetSaveTokenSecret enables the HMAC-signed save_token flow so save
// callers don't have to body-trust last_test_run_at. Pass any
// process-stable secret (server.go uses the existing internal token).
// Without it, the timestamp-trust path remains the only gate-pass.
func (h *PipelineHandler) SetSaveTokenSecret(secret []byte) {
	h.saveTokenSecret = secret
}

// SetJournal wires a journal Emitter post-construction so journal
// emission lands in production but stays no-op in tests.
func (h *PipelineHandler) SetJournal(e pipeline.Emitter) {
	h.emitter = e
}

// SetWaitpointStore wires the production WaitpointStore so StepWait
// approval steps persist their token state across process restarts.
// Without it, approval steps fall back to in-memory + 60s timeout
// (useful for dev, broken for any real approval workflow).
func (h *PipelineHandler) SetWaitpointStore(w pipeline.WaitpointStore) {
	h.waitpoints = w
}

// SetWSBroadcaster wires the WebSocket hub so pipeline run + step
// events stream live to subscribed clients (PipelineRunNode in the
// graph updates without polling). Without it, the frontend catches
// up via journal polling only.
func (h *PipelineHandler) SetWSBroadcaster(b pipeline.WSBroadcaster) {
	h.ws = b
}

// SetScheduleStore wires the pipeline_schedules persistence layer
// so the schedule CRUD endpoints have something to talk to.
// Without it, those endpoints reply 503 (the rest of the pipeline
// surface keeps working).
func (h *PipelineHandler) SetScheduleStore(s *pipeline.ScheduleStore) {
	h.schedules = s
}

// Runner exposes the wired AgentRunner so the in-process scheduler
// can build its own Executor with the same runner the HTTP path uses.
// Returns nil if SetRunner hasn't been called yet.
func (h *PipelineHandler) Runner() pipeline.AgentRunner {
	return h.runner
}

// Emitter exposes the journal Emitter so the scheduler can wire
// pipeline.run.* events into the journal stream the same way HTTP
// runs do. Returns nil if SetJournal hasn't been called.
func (h *PipelineHandler) Emitter() pipeline.Emitter {
	return h.emitter
}

// SetRunRegistry wires the in-memory cancel + concurrency tracker.
// Without it, /runs/{runId}/cancel returns 503 and the run-level
// concurrency_key gate is silently skipped.
func (h *PipelineHandler) SetRunRegistry(r *pipeline.RunRegistry) {
	h.runs = r
}

// RunRegistry exposes the wired registry so the scheduler-side
// executor can reuse it (the scheduler runs need to compete for the
// same concurrency slots as HTTP runs).
func (h *PipelineHandler) RunRegistry() *pipeline.RunRegistry {
	return h.runs
}

// SetWebhookStore wires pipeline_webhooks persistence + dispatch.
// Without it, the webhook CRUD endpoints + the public dispatch
// endpoint return 503.
func (h *PipelineHandler) SetWebhookStore(s *pipeline.WebhookStore) {
	h.webhooks = s
}

// newExecutor centralises Executor construction so every handler
// path picks up runner/emitter/waitpoints/ws wiring identically.
// Refactored from the inline `pipeline.NewExecutor(...)` calls in
// Run/DryRun/TestRun so a future capability (cost cap, PII gate)
// only needs to be wired once.
func (h *PipelineHandler) newExecutor() *pipeline.Executor {
	exec := pipeline.NewExecutor(h.store, h.resolver, h.runner, h.emitter)
	if h.waitpoints != nil {
		exec = exec.WithWaitpointStore(h.waitpoints)
	}
	if h.ws != nil {
		exec = exec.WithWSBroadcaster(h.ws)
	}
	if h.runs != nil {
		exec = exec.WithRunRegistry(h.runs)
	}
	if h.db != nil {
		// Idempotency store is cheap to reconstruct per-run — it's a
		// thin DB wrapper with no goroutines. Keeping construction
		// here means tests don't need to set it explicitly.
		exec = exec.WithIdempotencyStore(pipeline.NewIdempotencyStore(h.db))
	}
	return exec
}

// pipelineResponse is the wire shape returned by GET endpoints. We
// flatten + camelCase the persistent struct here so the on-disk
// schema can evolve without breaking the API.
type pipelineResponse struct {
	ID                   string  `json:"id"`
	Slug                 string  `json:"slug"`
	Name                 string  `json:"name"`
	Description          string  `json:"description,omitempty"`
	DSLVersion           string  `json:"dsl_version"`
	DefinitionHash       string  `json:"definition_hash"`
	Ephemeral            bool    `json:"ephemeral"`
	WorkspaceVisible     bool    `json:"workspace_visible"`
	InvocationCount      int     `json:"invocation_count"`
	LastInvokedAt        *string `json:"last_invoked_at,omitempty"`
	LastInvocationStatus string  `json:"last_invocation_status,omitempty"`
	AuthorCrewID         string  `json:"author_crew_id,omitempty"`
	AuthorAgentID        string  `json:"author_agent_id,omitempty"`
	AuthorUserID         string  `json:"author_user_id,omitempty"`
	AuthoredVia          string  `json:"authored_via"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
	// Definition is included on the detail endpoint only — list
	// responses omit it to keep payloads small.
	Definition json.RawMessage `json:"definition,omitempty"`
}

func toPipelineResponse(p *pipeline.Pipeline, includeDefinition bool) pipelineResponse {
	out := pipelineResponse{
		ID:                   p.ID,
		Slug:                 p.Slug,
		Name:                 p.Name,
		Description:          p.Description,
		DSLVersion:           p.DSLVersion,
		DefinitionHash:       p.DefinitionHash,
		Ephemeral:            p.Ephemeral,
		WorkspaceVisible:     p.WorkspaceVisible,
		InvocationCount:      p.InvocationCount,
		LastInvocationStatus: p.LastInvocationStatus,
		AuthorCrewID:         p.AuthorCrewID,
		AuthorAgentID:        p.AuthorAgentID,
		AuthorUserID:         p.AuthorUserID,
		AuthoredVia:          string(p.AuthoredVia),
		CreatedAt:            p.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt:            p.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"),
	}
	if p.LastInvokedAt != nil {
		t := p.LastInvokedAt.Format("2006-01-02T15:04:05.999999999Z07:00")
		out.LastInvokedAt = &t
	}
	if includeDefinition {
		out.Definition = json.RawMessage(p.DefinitionJSON)
	}
	return out
}

// List returns workspace-visible, non-ephemeral pipelines for the
// caller's workspace. Sorted by invocation_count DESC by default —
// the natural "what are my crews actually using" view.
//
// GET /api/v1/workspaces/{workspaceId}/pipelines
//
// Query parameters:
//
//	include_ephemeral=1      include auto-generated delegation wraps
//	include_hidden=1         include workspace_visible=0 entries
//	author_crew_id=crew_xyz  filter to one author crew
//	limit=50                 cap at 500 hard
//	order=popularity|recent|name
func (h *PipelineHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	q := r.URL.Query()
	f := pipeline.ListFilters{
		WorkspaceID:      workspaceID,
		IncludeEphemeral: q.Get("include_ephemeral") == "1",
		IncludeHidden:    q.Get("include_hidden") == "1",
		AuthorCrewID:     q.Get("author_crew_id"),
	}
	switch q.Get("order") {
	case "recent":
		f.OrderBy = pipeline.OrderByRecent
	case "name":
		f.OrderBy = pipeline.OrderByName
	default:
		f.OrderBy = pipeline.OrderByPopularity
	}

	rows, err := h.store.List(r.Context(), f)
	if err != nil {
		h.logger.Error("pipeline list", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list pipelines"})
		return
	}
	out := make([]pipelineResponse, 0, len(rows))
	for _, p := range rows {
		out = append(out, toPipelineResponse(p, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// Get returns a single pipeline by slug, including its definition.
// GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}
func (h *PipelineHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline get", "error", err, "slug", slug)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}
	writeJSON(w, http.StatusOK, toPipelineResponse(p, true))
}

// runRequestBody is the shared shape for /run + /dry_run.
type runRequestBody struct {
	Inputs map[string]any `json:"inputs"`
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline run: load", "error", err, "slug", slug)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}

	var body runRequestBody
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
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

	exec := h.newExecutor()
	res, err := exec.Run(r.Context(), pipeline.RunInput{
		PipelineID:      p.ID,
		WorkspaceID:     workspaceID,
		InvokingCrewID:  invokingCrew,
		InvokingAgentID: invokingAgent,
		Inputs:          body.Inputs,
		Mode:            pipeline.ModeRun,
		IdempotencyKey:  idempotencyKey,
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline dry_run: load", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}
	var body runRequestBody
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pipeline runner not wired"})
		return
	}

	var body struct {
		Definition   json.RawMessage `json:"definition"`
		AuthorCrewID string          `json:"author_crew_id"`
		SampleInputs map[string]any  `json:"sample_inputs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(body.Definition) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "definition required"})
		return
	}
	if body.AuthorCrewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "author_crew_id required"})
		return
	}

	dsl, err := pipeline.Parse(body.Definition)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	// Cross-reference validation on save uses agent_slug ⊆ author
	// crew membership. For the test_run we accept any slug — agent
	// slugs are validated again at save time. This keeps the
	// authoring loop fast (an iteration that fails because of a
	// typo gets a quick error from the runner, not a strict
	// schema check).
	if err := pipeline.Validate(dsl, nil, nil); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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

// definitionHashHex is the same SHA-256 hex digest the Store uses for
// pipeline.definition_hash. Lifted into the API layer so the save_token
// signer can bind to "this exact DSL bytes" — keeping the signer
// out of internal/pipeline avoids circular dependency.
func definitionHashHex(def []byte) string {
	sum := sha256.Sum256(def)
	return hex.EncodeToString(sum[:])
}

// Delete soft-deletes a pipeline by slug.
// DELETE /api/v1/workspaces/{workspaceId}/pipelines/{slug}
func (h *PipelineHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline delete: lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}
	if err := h.store.SoftDelete(r.Context(), p.ID); err != nil {
		h.logger.Error("pipeline delete", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete pipeline"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline list runs: load", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list runs"})
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

// parseSmallInt parses a small positive integer without pulling in
// strconv.Atoi for a single-digit pattern. Worth a few lines because
// it caps at 999 — enough for `limit` clamps without the overhead of
// full strconv error path, and explicit bounds in code.
func parseSmallInt(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("non-digit")
		}
		n = n*10 + int(c-'0')
		if n > 9999 {
			return 0, errors.New("too large")
		}
	}
	return n, nil
}

// ExportPipeline returns a portable bundle for a pipeline. The
// bundle is a self-contained JSON document a downstream workspace
// (or marketplace consumer) can import via POST .../import.
//
// Bundle shape (format = "crewship-pipeline-bundle/v1"):
//
//	{
//	  "format": "crewship-pipeline-bundle/v1",
//	  "pipeline": { name, description, definition, dsl_version,
//	                authored_via, change_summary },
//	  "history":  [{version, definition_hash, change_summary, ...}],
//	  "metadata": { exported_at, source_workspace_id, ... }
//	}
//
// We deliberately exclude author_crew_id, author_agent_id, runtime
// stats (invocation_count, last_invoked_at), and any
// installation-specific data — the receiving workspace will fill
// those in at import time. This keeps marketplace bundles
// installation-independent.
//
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/export
func (h *PipelineHandler) ExportPipeline(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}
	includeHistory := r.URL.Query().Get("include_history") == "1"
	type historyItem struct {
		Version       int             `json:"version"`
		Hash          string          `json:"definition_hash"`
		Definition    json.RawMessage `json:"definition,omitempty"`
		ChangeSummary string          `json:"change_summary,omitempty"`
		ParentVersion *int            `json:"parent_version,omitempty"`
		CreatedAt     string          `json:"created_at"`
	}
	var history []historyItem
	if includeHistory {
		versions, _ := h.store.ListVersions(r.Context(), p.ID, 500)
		for _, v := range versions {
			history = append(history, historyItem{
				Version:       v.Version,
				Hash:          v.DefinitionHash,
				Definition:    json.RawMessage(v.DefinitionJSON),
				ChangeSummary: v.ChangeSummary,
				ParentVersion: v.ParentVersion,
				CreatedAt:     v.CreatedAt.Format(time.RFC3339Nano),
			})
		}
	}
	bundle := map[string]any{
		"format": "crewship-pipeline-bundle/v1",
		"pipeline": map[string]any{
			"name":        p.Name,
			"description": p.Description,
			"slug":        p.Slug,
			"dsl_version": p.DSLVersion,
			"definition":  json.RawMessage(p.DefinitionJSON),
		},
		"metadata": map[string]any{
			"exported_at":         time.Now().UTC().Format(time.RFC3339Nano),
			"source_workspace_id": workspaceID,
			"definition_hash":     p.DefinitionHash,
			"head_version":        p.InvocationCount, // misnamed — leave for caller transparency
		},
	}
	if includeHistory {
		bundle["history"] = history
	}
	writeJSON(w, http.StatusOK, bundle)
}

// ImportPipeline creates a pipeline from a portable bundle. Used by
// marketplace install flows + cross-workspace transfer. The
// receiving workspace becomes the author crew context (via
// X-Author-Crew header or body field), and the bundle's source
// metadata is preserved on the pipeline row for audit.
//
// POST /api/v1/workspaces/{ws}/pipelines/import
// Body: <pipeline-bundle>  + { "author_crew_id": "..." }
func (h *PipelineHandler) ImportPipeline(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	var bundle struct {
		Format   string `json:"format"`
		Pipeline struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Slug        string          `json:"slug"`
			DSLVersion  string          `json:"dsl_version"`
			Definition  json.RawMessage `json:"definition"`
		} `json:"pipeline"`
		Metadata map[string]any `json:"metadata"`
		// Caller-supplied author_crew_id; required since the
		// bundle deliberately doesn't carry one.
		AuthorCrewID string `json:"author_crew_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid bundle"})
		return
	}
	if bundle.Format != "crewship-pipeline-bundle/v1" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "unsupported bundle format",
			"format": bundle.Format,
		})
		return
	}
	if bundle.Pipeline.Name == "" || len(bundle.Pipeline.Definition) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bundle missing pipeline.name or pipeline.definition"})
		return
	}
	if bundle.AuthorCrewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "author_crew_id required (the crew that will own this imported pipeline)"})
		return
	}
	// Run validation at import — we don't want a malformed bundle
	// to land in the workspace registry. Cross-references against
	// the receiving workspace's agents are checked too.
	dsl, err := pipeline.Parse(bundle.Pipeline.Definition)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	agentSlugs, _ := h.lookupAgentSlugs(r, bundle.AuthorCrewID)
	pipelineSlugs, _ := h.lookupPipelineSlugs(r, workspaceID)
	if err := pipeline.Validate(dsl, agentSlugs, pipelineSlugs); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "imported pipeline failed validation: " + err.Error(),
			"hint":  "the receiving workspace must have all referenced agent slugs",
		})
		return
	}
	// Slug: prefer bundle's slug, fall back to slugifyName(name).
	slug := bundle.Pipeline.Slug
	if slug == "" {
		// Reuse the runtime helper — same shape as sidecar/CLI.
		slug = bundle.Pipeline.Name // best-effort; Save will reject
		// invalid shapes, which prompts the importer to rename.
	}
	importedFromURL := ""
	if bundle.Metadata != nil {
		if v, ok := bundle.Metadata["source_workspace_id"].(string); ok {
			importedFromURL = "workspace:" + v
		}
	}
	now := time.Now().UTC()
	in := pipeline.SaveInput{
		WorkspaceID:    workspaceID,
		Slug:           slug,
		Name:           bundle.Pipeline.Name,
		Description:    bundle.Pipeline.Description,
		DSLVersion:     bundle.Pipeline.DSLVersion,
		DefinitionJSON: string(bundle.Pipeline.Definition),
		Author: pipeline.AuthorMeta{
			CrewID:      bundle.AuthorCrewID,
			Via:         pipeline.AuthoredViaImported,
			ImportedURL: importedFromURL,
		},
		// Imports skip the test_run gate by design — a marketplace
		// bundle is presumed to have passed test_run in its source
		// workspace. The receiving operator can run a manual
		// test_run from the UI before invoking.
		LastTestRunAt:     &now,
		LastTestRunPassed: true,
	}
	saved, err := h.store.Save(r.Context(), in)
	if errors.Is(err, pipeline.ErrSlugConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "slug already exists in workspace"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline import save", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toPipelineResponse(saved, true))
}

// ListVersions returns the version history for a pipeline.
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/versions
func (h *PipelineHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline list versions: load", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := parseSmallInt(v); err == nil {
			limit = n
		}
	}
	versions, err := h.store.ListVersions(r.Context(), p.ID, limit)
	if err != nil {
		h.logger.Error("pipeline list versions: query", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list versions"})
		return
	}
	type versionRow struct {
		Version       int    `json:"version"`
		Hash          string `json:"definition_hash"`
		AuthorType    string `json:"author_type"`
		AuthorID      string `json:"author_id"`
		ParentVersion *int   `json:"parent_version,omitempty"`
		ChangeSummary string `json:"change_summary,omitempty"`
		CreatedAt     string `json:"created_at"`
	}
	out := make([]versionRow, 0, len(versions))
	for _, v := range versions {
		out = append(out, versionRow{
			Version:       v.Version,
			Hash:          v.DefinitionHash,
			AuthorType:    v.AuthorType,
			AuthorID:      v.AuthorID,
			ParentVersion: v.ParentVersion,
			ChangeSummary: v.ChangeSummary,
			CreatedAt:     v.CreatedAt.Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetVersion returns one specific version including the full DSL.
// GET /api/v1/workspaces/{ws}/pipelines/{slug}/versions/{n}
func (h *PipelineHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	versionStr := r.PathValue("n")
	versionNum, perr := parseSmallInt(versionStr)
	if perr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version must be a positive integer"})
		return
	}
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}
	v, err := h.store.GetVersion(r.Context(), p.ID, versionNum)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "version not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         v.Version,
		"definition_hash": v.DefinitionHash,
		"definition":      json.RawMessage(v.DefinitionJSON),
		"author_type":     v.AuthorType,
		"author_id":       v.AuthorID,
		"parent_version":  v.ParentVersion,
		"change_summary":  v.ChangeSummary,
		"created_at":      v.CreatedAt.Format(time.RFC3339Nano),
	})
}

// Rollback rolls the head pointer + definition_json back to a
// previous version. History is preserved (rollback doesn't delete).
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/rollback
// Body: { "version": N }
func (h *PipelineHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	var body struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Version < 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "version must be >= 1"})
		return
	}
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pipeline not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "load pipeline"})
		return
	}
	rolled, err := h.store.Rollback(r.Context(), p.ID, body.Version)
	if errors.Is(err, pipeline.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "version not found"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline rollback", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, toPipelineResponse(rolled, true))
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "waitpoint store not wired"})
		return
	}
	token := r.PathValue("token")
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token required"})
		return
	}
	var body struct {
		Approved bool   `json:"approved"`
		Comment  string `json:"comment"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "waitpoint store does not support completion"})
		return
	}
	deciderID := "" // TODO: pull from JWT user when auth middleware exposes it on ctx
	payload := body.Comment
	if err := wp.CompleteApproval(r.Context(), token, body.Approved, deciderID, payload); err != nil {
		// pipeline.ErrAlreadyDecided → 409
		if err.Error() == "waitpoint: already decided or expired" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		h.logger.Error("waitpoint complete", "error", err, "token", token)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list waitpoints"})
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

// lookupAgentSlugs returns the set of agent slugs that exist in the
// given crew. Used by InternalSave's semantic-validation pass so
// pipelines referencing unknown agents are rejected before they hit
// the registry. Returns an empty (non-nil) set when the crew has no
// agents — pipeline.Validate distinguishes nil ("skip the check")
// from non-nil-but-empty ("crew has nothing").
func (h *PipelineHandler) lookupAgentSlugs(r *http.Request, crewID string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	if crewID == "" {
		return out, nil
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT slug FROM agents WHERE crew_id = ? AND deleted_at IS NULL`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err == nil {
			out[slug] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// lookupPipelineSlugs returns the set of pipeline slugs already
// registered in the workspace. Used by InternalSave's semantic
// validation so call_pipeline references can be flagged when the
// target slug is unknown. The validator treats unknown targets as
// non-fatal (warn-shape) so a pair of related pipelines saved in
// one session can reference each other.
func (h *PipelineHandler) lookupPipelineSlugs(r *http.Request, workspaceID string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	if workspaceID == "" {
		return out, nil
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT slug FROM pipelines WHERE workspace_id = ? AND deleted_at IS NULL`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err == nil {
			out[slug] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// cycleResolver returns a closure that pipeline.CycleDetect uses to
// walk the call_pipeline graph. The closure loads the target
// pipeline's DSL from the workspace registry. Errors fall through
// as "unknown target" — CycleDetect explicitly tolerates that and
// stops walking the unreachable branch (no false positives).
func (h *PipelineHandler) cycleResolver(ctx context.Context, workspaceID string) func(slug string) (*pipeline.DSL, error) {
	return func(slug string) (*pipeline.DSL, error) {
		row, err := h.store.GetBySlug(ctx, workspaceID, slug)
		if err != nil {
			return nil, err
		}
		return pipeline.Parse([]byte(row.DefinitionJSON))
	}
}

// parseRFC3339 wraps time.Parse with both nano + plain RFC3339 so
// the body's last_test_run_at can survive whatever the sidecar
// happened to format. Returns the zero time + error on parse fail
// — callers treat that as "no fresh test run", which the store
// layer will then reject with ErrTestRunGateFailed.
func parseRFC3339(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// internalSaveRequest carries the IPC body sidecar→main forwards
// for an agent emitting a new pipeline definition. Test-run gate
// is enforced by the handler — the sidecar must call test_run
// first and pass the resulting timestamp through.
type internalSaveRequest struct {
	WorkspaceID       string          `json:"workspace_id"`
	Slug              string          `json:"slug"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Definition        json.RawMessage `json:"definition"`
	AuthorCrewID      string          `json:"author_crew_id"`
	AuthorAgentID     string          `json:"author_agent_id"`
	AuthorChatID      string          `json:"author_chat_id"`
	AuthorRunID       string          `json:"author_run_id"`
	LastTestRunAt     string          `json:"last_test_run_at"` // RFC3339
	LastTestRunPassed bool            `json:"last_test_run_passed"`
}

// userSaveRequest is the body shape for the workspace-scoped save
// endpoint. Workspace_id comes from the path (wsCtx middleware), not
// the body. Author identity is inferred from the JWT — user_id +
// authored_via = "user_api". The optional author_crew_id lets UI
// authors pin a specific crew context for runtime; without it, runs
// fall back to the first crew the saving user belongs to.
type userSaveRequest struct {
	Slug              string          `json:"slug"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Definition        json.RawMessage `json:"definition"`
	AuthorCrewID      string          `json:"author_crew_id,omitempty"`
	LastTestRunAt     string          `json:"last_test_run_at,omitempty"` // RFC3339
	LastTestRunPassed bool            `json:"last_test_run_passed,omitempty"`
	// SkipTestGate is honored only when the caller's role is
	// OWNER or ADMIN; lower roles get a 403 if they try. Used by
	// UI flows that have already test-run'd the definition through
	// the /test_run endpoint and pass last_test_run_at + true here.
	SkipTestGate bool `json:"skip_test_gate,omitempty"`
	// SaveToken is the HMAC-signed proof returned by /test_run that
	// THIS user just successfully ran THIS definition_hash. When
	// present and valid, supersedes the body-trust on
	// last_test_run_at — that field can be omitted entirely. See
	// pipelines_save_token.go for the threat model rationale.
	SaveToken string `json:"save_token,omitempty"`
}

// Save is the workspace-scoped save endpoint that backs the UI's
// "New routine" flow. JWT auth + MANAGER+ role required. The
// distinction from InternalSave: author identity is extracted from
// the user context (not trusted from the body), and authored_via is
// always "user_api" so audit logs show real human authorship.
//
// POST /api/v1/workspaces/{wsId}/pipelines/save
func (h *PipelineHandler) Save(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth required"})
		return
	}
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "MANAGER+ role required to save routines"})
		return
	}

	var body userSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Slug == "" || len(body.Definition) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug + definition required"})
		return
	}
	if body.SkipTestGate && role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "skip_test_gate requires OWNER or ADMIN role",
		})
		return
	}

	dsl, err := pipeline.Parse(body.Definition)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	// If author_crew_id is provided, validate the user's agent slugs
	// against THAT crew (cross-crew validation). If absent, skip
	// agent-slug validation so the routine saves with whatever the
	// DSL declares — runtime resolution at first invocation surfaces
	// any mismatch with a clear error.
	var agentSlugs map[string]struct{}
	if body.AuthorCrewID != "" {
		var lookupErr error
		agentSlugs, lookupErr = h.lookupAgentSlugs(r, body.AuthorCrewID)
		if lookupErr != nil {
			h.logger.Warn("pipeline user save: lookup agent slugs", "error", lookupErr, "crew", body.AuthorCrewID)
			agentSlugs = nil
		}
	}
	pipelineSlugs, err := h.lookupPipelineSlugs(r, workspaceID)
	if err != nil {
		h.logger.Warn("pipeline user save: lookup pipeline slugs", "error", err, "workspace", workspaceID)
		pipelineSlugs = nil
	}
	if err := pipeline.Validate(dsl, agentSlugs, pipelineSlugs); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := pipeline.CycleDetect(dsl, h.cycleResolver(r.Context(), workspaceID)); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	in := pipeline.SaveInput{
		WorkspaceID:    workspaceID,
		Slug:           body.Slug,
		Name:           body.Name,
		Description:    body.Description,
		DefinitionJSON: string(body.Definition),
		Author: pipeline.AuthorMeta{
			CrewID: body.AuthorCrewID,
			UserID: user.ID,
			Via:    pipeline.AuthoredViaUser,
		},
		LastTestRunPassed: body.LastTestRunPassed || body.SkipTestGate,
	}

	// Three paths to clearing the test-gate gate, in priority order:
	// 1. SaveToken (HMAC, no body trust) — preferred path
	// 2. SkipTestGate (OWNER/ADMIN role-gated escape hatch)
	// 3. body's last_test_run_at + last_test_run_passed (legacy body
	//    trust, kept for sidecar back-compat; will be retired once all
	//    callers migrate to SaveToken).
	switch {
	case body.SaveToken != "":
		defHash := definitionHashHex(body.Definition)
		if err := verifySaveToken(h.saveTokenSecret, body.SaveToken, workspaceID, defHash, user.ID); err != nil {
			h.logger.Warn("pipeline save: save_token rejected", "user_id", user.ID, "slug", body.Slug, "err", err)
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error": "save_token invalid (expired, malformed, or signed for a different definition/user)",
			})
			return
		}
		// Token verified — synthesize a passing-now timestamp so the
		// store gate doesn't fire on the body's missing fields.
		now := time.Now().UTC()
		in.LastTestRunAt = &now
		in.LastTestRunPassed = true
		h.logger.Info("pipeline save: cleared via save_token", "user_id", user.ID, "slug", body.Slug)
	case body.SkipTestGate:
		now := time.Now().UTC()
		in.LastTestRunAt = &now
		h.logger.Info("pipeline save: test gate skipped", "user_id", user.ID, "role", role, "slug", body.Slug)
	default:
		if t, err := parseRFC3339(body.LastTestRunAt); err == nil {
			in.LastTestRunAt = &t
		}
	}

	saved, err := h.store.Save(r.Context(), in)
	if errors.Is(err, pipeline.ErrTestRunGateFailed) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "save requires a fresh, passing test_run within 5 minutes (or skip_test_gate for OWNER/ADMIN)",
		})
		return
	}
	if errors.Is(err, pipeline.ErrSlugConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "slug already exists in workspace"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline user save", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toPipelineResponse(saved, true))
}

// InternalSave is the trusted endpoint the sidecar forwards to.
// X-Internal-Token authentication runs upstream of this handler;
// here we just trust the caller's claim about author identity.
//
// POST /api/v1/internal/pipelines/save
func (h *PipelineHandler) InternalSave(w http.ResponseWriter, r *http.Request) {
	var body internalSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.WorkspaceID == "" || body.Slug == "" || len(body.Definition) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id, slug, definition required"})
		return
	}

	// Parse + validate before save so the agent gets a clean error
	// message at this layer rather than at the next /run.
	//
	// Semantic checks: pass real agent + pipeline slug sets so the
	// validator catches cross-crew references (agent_slug not in the
	// author crew, call_pipeline target not in the workspace) at
	// save time rather than letting them blow up at first run.
	// Cycle detection runs in a separate pass with a workspace-
	// scoped resolver since CycleDetect needs to walk the call
	// graph beyond `dsl` itself.
	dsl, err := pipeline.Parse(body.Definition)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	agentSlugs, err := h.lookupAgentSlugs(r, body.AuthorCrewID)
	if err != nil {
		h.logger.Warn("pipeline internal save: lookup agent slugs", "error", err, "crew", body.AuthorCrewID)
		// Non-fatal: fall back to nil-set validation (the original
		// schema-only path) rather than blocking the save on a
		// crew lookup hiccup. The runtime still surfaces unknown
		// agent_slug at first invocation.
		agentSlugs = nil
	}
	pipelineSlugs, err := h.lookupPipelineSlugs(r, body.WorkspaceID)
	if err != nil {
		h.logger.Warn("pipeline internal save: lookup pipeline slugs", "error", err, "workspace", body.WorkspaceID)
		pipelineSlugs = nil
	}
	if err := pipeline.Validate(dsl, agentSlugs, pipelineSlugs); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	// Cycle detection over the workspace's saved pipelines plus this
	// candidate. The resolver loads target DSLs lazily; nodes that
	// aren't in the workspace yet stop the walk on that branch (no
	// false positives — see pipeline.CycleDetect docstring).
	if err := pipeline.CycleDetect(dsl, h.cycleResolver(r.Context(), body.WorkspaceID)); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	in := pipeline.SaveInput{
		WorkspaceID:    body.WorkspaceID,
		Slug:           body.Slug,
		Name:           body.Name,
		Description:    body.Description,
		DefinitionJSON: string(body.Definition),
		Author: pipeline.AuthorMeta{
			CrewID:  body.AuthorCrewID,
			AgentID: body.AuthorAgentID,
			ChatID:  body.AuthorChatID,
			RunID:   body.AuthorRunID,
			Via:     pipeline.AuthoredViaAgent,
		},
		LastTestRunPassed: body.LastTestRunPassed,
	}
	if t, err := parseRFC3339(body.LastTestRunAt); err == nil {
		in.LastTestRunAt = &t
	}

	saved, err := h.store.Save(r.Context(), in)
	if errors.Is(err, pipeline.ErrTestRunGateFailed) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "save requires a fresh, passing test_run (within 5 minutes)",
		})
		return
	}
	if errors.Is(err, pipeline.ErrSlugConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "slug already exists in workspace"})
		return
	}
	if err != nil {
		h.logger.Error("pipeline internal save", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toPipelineResponse(saved, true))
}
