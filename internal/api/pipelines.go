package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
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
	runStore   *pipeline.RunStore      // optional; nil → list-runs falls back to journal LIKE scan, no persistence
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

// SetRunStore wires the pipeline_runs persistence layer (migration
// v83). The executor created via newExecutor in this handler picks
// up the store via WithRunStore, and the ListRuns API hits this
// store directly when present (column-typed reads beat LIKE-scanning
// journal_entries). Without it, runs persist only in journal_entries
// and list-runs falls back to the legacy scan path.
func (h *PipelineHandler) SetRunStore(s *pipeline.RunStore) {
	h.runStore = s
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
	if h.runStore != nil {
		exec = exec.WithRunStore(h.runStore)
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
	// AuthorAgentName denormalizes the author agent's display name so
	// the routines list can render "Authored by Eva" without a second
	// fetch + client-side join. Populated by the List handler via a
	// batch lookup; empty when AuthorAgentID is empty or the agent
	// was deleted.
	AuthorAgentName string `json:"author_agent_name,omitempty"`
	AuthorUserID    string `json:"author_user_id,omitempty"`
	AuthoredVia     string `json:"authored_via"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	// LinkedIssueCount is the number of issues bound to this routine
	// via missions.routine_id. LinkedIssues holds up to the first 3
	// issue identifiers so the UI can render a "ENG-5, ENG-9 +1"
	// chip without paginating. Both populated by the List handler
	// from a single GROUP BY query.
	LinkedIssueCount int      `json:"linked_issue_count"`
	LinkedIssues     []string `json:"linked_issues,omitempty"`
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

// enrichPipelineListAuthorNames looks up the display name for each
// distinct author_agent_id in the response and stitches it back onto
// the matching rows. Best-effort: a SQL error or a missing agent
// just leaves AuthorAgentName empty.
func enrichPipelineListAuthorNames(ctx context.Context, db *sql.DB, logger *slog.Logger, rows []pipelineResponse) {
	if len(rows) == 0 {
		return
	}
	idSet := make(map[string]struct{})
	for _, r := range rows {
		if r.AuthorAgentID != "" {
			idSet[r.AuthorAgentID] = struct{}{}
		}
	}
	if len(idSet) == 0 {
		return
	}
	ids := make([]any, 0, len(idSet))
	placeholders := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
		placeholders = append(placeholders, "?")
	}
	// Exclude soft-deleted agents — same defensive scope as
	// lookupAgentSlugs. A name resolved through this enrichment is
	// shown to the user; surfacing the name of an agent the operator
	// removed would be confusing.
	q := `SELECT id, name FROM agents WHERE deleted_at IS NULL AND id IN (` + strings.Join(placeholders, ",") + `)`
	res, err := db.QueryContext(ctx, q, ids...)
	if err != nil {
		logger.Warn("pipeline list: author name lookup", "error", err)
		return
	}
	defer res.Close()
	names := make(map[string]string, len(idSet))
	scanErrors := 0
	for res.Next() {
		var id, name string
		if scanErr := res.Scan(&id, &name); scanErr != nil {
			scanErrors++
			continue
		}
		names[id] = name
	}
	// Iterator-level error after the loop catches driver-side
	// problems (broken connection, decode error mid-stream) that
	// res.Next() swallowed silently.
	if rowsErr := res.Err(); rowsErr != nil {
		logger.Warn("pipeline list: author name iterator", "error", rowsErr)
	}
	if scanErrors > 0 {
		logger.Warn("pipeline list: author name scans skipped", "count", scanErrors)
	}
	for i := range rows {
		if n, ok := names[rows[i].AuthorAgentID]; ok {
			rows[i].AuthorAgentName = n
		}
	}
}

// enrichPipelineListLinkedIssues counts issues bound to each pipeline
// via missions.routine_id and inlines up to 3 identifiers per routine
// so the UI can render a chip without a second fetch. Best-effort:
// SQL errors leave the counts at 0 rather than failing the request.
func enrichPipelineListLinkedIssues(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID string, rows []pipelineResponse) {
	if len(rows) == 0 {
		return
	}
	idSet := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		idSet[r.ID] = struct{}{}
	}
	if len(idSet) == 0 {
		return
	}
	ids := make([]any, 0, len(idSet)+1)
	ids = append(ids, workspaceID)
	placeholders := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
		placeholders = append(placeholders, "?")
	}
	// Identifiers ordered by created_at DESC so the truncated set is
	// the *recent* bindings — those are the ones a user is likeliest
	// to want to revisit. The ROW_NUMBER trick keeps the list small
	// per routine_id.
	q := `
SELECT routine_id, identifier
FROM (
  SELECT routine_id, identifier,
    ROW_NUMBER() OVER (PARTITION BY routine_id ORDER BY created_at DESC) AS rn
  FROM missions
  WHERE workspace_id = ?
    AND routine_id IN (` + strings.Join(placeholders, ",") + `)
    AND identifier IS NOT NULL
)
WHERE rn <= 3
ORDER BY routine_id, rn`
	res, err := db.QueryContext(ctx, q, ids...)
	if err != nil {
		logger.Warn("pipeline list: linked issues lookup", "error", err)
		return
	}
	defer res.Close()
	type bucket struct {
		count       int
		identifiers []string
	}
	linked := make(map[string]*bucket)
	for res.Next() {
		var routineID, identifier string
		if scanErr := res.Scan(&routineID, &identifier); scanErr != nil {
			logger.Warn("pipeline list: scan linked issue", "error", scanErr)
			continue
		}
		b, ok := linked[routineID]
		if !ok {
			b = &bucket{}
			linked[routineID] = b
		}
		b.identifiers = append(b.identifiers, identifier)
	}
	// Second query to capture totals — the windowed query above caps
	// at 3 per routine, so we need a bare COUNT for the badge number.
	q2 := `SELECT routine_id, COUNT(*) FROM missions
		WHERE workspace_id = ? AND routine_id IN (` + strings.Join(placeholders, ",") + `)
		GROUP BY routine_id`
	res2, err := db.QueryContext(ctx, q2, ids...)
	if err != nil {
		logger.Warn("pipeline list: linked count lookup", "error", err)
		return
	}
	defer res2.Close()
	for res2.Next() {
		var routineID string
		var count int
		if scanErr := res2.Scan(&routineID, &count); scanErr != nil {
			continue
		}
		b, ok := linked[routineID]
		if !ok {
			b = &bucket{}
			linked[routineID] = b
		}
		b.count = count
	}
	for i := range rows {
		if b, ok := linked[rows[i].ID]; ok {
			rows[i].LinkedIssueCount = b.count
			rows[i].LinkedIssues = b.identifiers
		}
	}
}

// definitionHashHex delegates to the pipeline package's exported
// DefinitionHash so the save_token signer always agrees with the
// Store's stored hash. Single-source-of-truth — the previous separate
// implementation here could drift from store.go and silently break
// save_token verification.
func definitionHashHex(def []byte) string {
	return pipeline.DefinitionHash(def)
}

// truncateErrorForList sanitizes an error_message before exposing it
// through the run-records list endpoint. Caller-supplied + executor-
// supplied error strings can carry: file paths, stack frames, half-
// rendered prompts that included secrets, full credential values
// that the must_not_contain gate didn't catch. The list view doesn't
// need that detail — operators drill into journal_entries via the
// /runs?include_steps=1 endpoint when they want the full picture.
func truncateErrorForList(s string) string {
	if s == "" {
		return ""
	}
	// First newline = stop. Keeps single-line summaries intact, drops
	// multi-line stack traces.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	// Hard length cap. UTF-8 safe slice — walk back to a rune
	// boundary so we don't emit invalid bytes.
	const cap = 200
	if len(s) <= cap {
		return s
	}
	cut := cap
	for cut > 0 && cut > cap-4 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
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
