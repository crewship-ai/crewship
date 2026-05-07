package api

import (
	"database/sql"
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
	db       *sql.DB
	logger   *slog.Logger
	store    *pipeline.Store
	resolver *pipeline.Resolver
	runner   pipeline.AgentRunner
	emitter  pipeline.Emitter
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

// SetJournal wires a journal Emitter post-construction so journal
// emission lands in production but stays no-op in tests.
func (h *PipelineHandler) SetJournal(e pipeline.Emitter) {
	h.emitter = e
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

	exec := pipeline.NewExecutor(h.store, h.resolver, h.runner, h.emitter)
	res, err := exec.Run(r.Context(), pipeline.RunInput{
		PipelineID:      p.ID,
		WorkspaceID:     workspaceID,
		InvokingCrewID:  invokingCrew,
		InvokingAgentID: invokingAgent,
		Inputs:          body.Inputs,
		Mode:            pipeline.ModeRun,
	})
	if err != nil {
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
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	exec := pipeline.NewExecutor(h.store, h.resolver, h.runner, h.emitter)
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

	exec := pipeline.NewExecutor(h.store, h.resolver, h.runner, h.emitter)
	res, err := exec.RunDefinition(r.Context(), dsl, pipeline.RunInput{
		WorkspaceID:  workspaceID,
		AuthorCrewID: body.AuthorCrewID,
		Inputs:       body.SampleInputs,
		Mode:         pipeline.ModeTestRun,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
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

	// We index pipeline runs purely through journal_entries.
	// payload->>'pipeline_id' is the join column; SQLite supports
	// json_extract since 3.38 which is ages ago. Filter to the
	// run-level entries only — step entries fan out per run, which
	// is too noisy for the index list.
	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, run_id_from_payload(payload) AS run_id, ts, entry_type, severity,
       summary, payload
FROM (
    SELECT id, ts, entry_type, severity, summary, payload,
           json_extract(payload, '$.pipeline_id') AS pid
    FROM journal_entries
    WHERE workspace_id = ?
      AND entry_type LIKE 'pipeline.run.%'
)
WHERE pid = ?
ORDER BY ts DESC
LIMIT ?`, workspaceID, p.ID, limit)
	if err != nil {
		// Fallback for SQLite builds without json_extract: pull all
		// pipeline.run.* entries for the workspace and filter in Go.
		// Cheap on workspaces with low pipeline activity; if it
		// becomes a hot path we'll add a stored generated column.
		rows, err = h.db.QueryContext(r.Context(), `
SELECT id, ts, entry_type, severity, summary, payload
FROM journal_entries
WHERE workspace_id = ? AND entry_type LIKE 'pipeline.run.%'
ORDER BY ts DESC
LIMIT ?`, workspaceID, limit*5)
		if err != nil {
			h.logger.Error("pipeline list runs: query", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list runs"})
			return
		}
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
			runIDExt   sql.NullString
			cols       = []any{&e.ID, &e.Timestamp, &e.EntryType, &e.Severity, &e.Summary, &payloadRaw}
		)
		// We only support the fallback 6-col query for now; the
		// computed run_id column is a Phase 2 optimisation that
		// requires a SQL function. Today, run_id is parsed from the
		// payload JSON below.
		_ = runIDExt
		if err := rows.Scan(cols...); err != nil {
			h.logger.Warn("pipeline list runs: scan", "error", err)
			continue
		}
		if runIDExt.Valid {
			e.RunID = runIDExt.String
		}
		// In the fallback path we filter pipeline_id client-side.
		var meta map[string]any
		if err := json.Unmarshal([]byte(payloadRaw), &meta); err == nil {
			if pid, ok := meta["pipeline_id"].(string); ok {
				if pid != p.ID {
					continue
				}
				e.PipelineID = pid
			}
			if rid, ok := meta["run_id"].(string); ok {
				e.RunID = rid
			}
		}
		e.Payload = json.RawMessage(payloadRaw)
		out = append(out, e)
		if len(out) >= limit {
			break
		}
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
	dsl, err := pipeline.Parse(body.Definition)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	if err := pipeline.Validate(dsl, nil, nil); err != nil {
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
