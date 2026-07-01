package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// RunHandler provides endpoints for listing and querying agent execution runs.
type RunHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewRunHandler creates a RunHandler with the given database and logger.
func NewRunHandler(db *sql.DB, logger *slog.Logger) *RunHandler {
	return &RunHandler{db: db, logger: logger}
}

type runResponse struct {
	ID           string          `json:"id"`
	AgentID      string          `json:"agent_id"`
	ChatID       *string         `json:"chat_id"`
	WorkspaceID  string          `json:"workspace_id"`
	TriggeredBy  *string         `json:"triggered_by"`
	TriggerType  string          `json:"trigger_type"`
	Status       string          `json:"status"`
	StartedAt    *string         `json:"started_at"`
	FinishedAt   *string         `json:"finished_at"`
	ErrorMessage *string         `json:"error_message"`
	ExitCode     *int            `json:"exit_code"`
	Metadata     json.RawMessage `json:"metadata"`
	// Model is the model the run actually resolved to (session-init ground
	// truth) — lets an operator verify Opus-vs-Sonnet on a subscription.
	// Omitted when unknown (older runs, non-Claude adapters).
	Model     *string `json:"model,omitempty"`
	CreatedAt string  `json:"created_at"`
	AgentName *string `json:"agent_name,omitempty"`
	AgentSlug *string `json:"agent_slug,omitempty"`
	CrewName  *string `json:"crew_name,omitempty"`
}

type runListResponse struct {
	Data       []runResponse `json:"data"`
	Stats      runStats      `json:"stats"`
	Pagination pagination    `json:"pagination"`
}

type runStats struct {
	Running int `json:"running"`
	Today   int `json:"today"`
	Failed  int `json:"failed"`
}

type pagination struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// Get returns a single run by id.
// GET /api/v1/runs/{id}
//
// Reads via journal.ListRuns with a workspace + agent filter scoped to a
// single trace_id; reuses the same enrichment as List so the response
// shape is identical to a List row. 404 when no run with that id exists
// in the caller's workspace — cross-tenant lookups are intentionally
// masked as 404 to avoid leaking the run's existence in another
// workspace.
func (h *RunHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "run id required")
		return
	}

	// journal.ListRuns doesn't accept a trace_id filter directly, so we
	// pull a small page and scan in-memory. The expected hit is page 1
	// (most lookups are recent), which the FE also relies on for its
	// stats badge.  This is bounded by limit=100 — a future
	// journal.GetRunByID can replace this when the surface grows.
	aggregated, _, err := journal.ListRuns(r.Context(), h.db, journal.RunsQuery{
		WorkspaceID: workspaceID,
		Limit:       100,
	})
	if err != nil {
		h.logger.Error("get run: list", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	var found *journal.RunAggregated
	for i := range aggregated {
		if aggregated[i].ID == id {
			found = &aggregated[i]
			break
		}
	}
	if found == nil {
		// Fallback: scan deeper pages up to a hard cap so older runs are
		// still resolvable by id. 1k entries ≈ 10 page sweeps; the cost is
		// bounded and the typical lookup is page 1.
		//
		// A query failure on a deeper page is a 500, NOT a 404 — silently
		// breaking out of the loop and returning "run not found" would
		// misreport a transient SQL error as the run not existing.
		for offset := 100; offset < 1000 && found == nil; offset += 100 {
			page, _, err := journal.ListRuns(r.Context(), h.db, journal.RunsQuery{
				WorkspaceID: workspaceID,
				Limit:       100,
				Offset:      offset,
			})
			if err != nil {
				h.logger.Error("get run: deep page", "error", err, "offset", offset)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if len(page) == 0 {
				break
			}
			for i := range page {
				if page[i].ID == id {
					found = &page[i]
					break
				}
			}
		}
	}
	if found == nil {
		replyError(w, http.StatusNotFound, "run not found")
		return
	}
	enriched := h.enrichRuns(r.Context(), workspaceID, []journal.RunAggregated{*found})
	if len(enriched) == 0 {
		replyError(w, http.StatusInternalServerError, "enrich failed")
		return
	}
	writeJSON(w, http.StatusOK, enriched[0])
}

// List returns a paginated list of agent runs in the workspace with stats and optional filters.
// GET /api/v1/runs
//
// Reads exclusively from journal_entries (grouped by trace_id) — agent_runs
// is being phased out (Phase J of unified-journal). Response shape is
// preserved 1:1 so frontend consumers don't see a contract change.
func (h *RunHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		// Without a workspace journal.ListRuns errors out as a 500, but
		// the caller's mistake is missing context — return 401 to match
		// every other read handler in this package and to avoid leaking
		// internal failure modes.
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	// Upper-clamp page before the (page-1)*limit multiply: an unbounded page
	// can overflow a signed int to a negative offset, confusing pagination.
	const maxPage = 1_000_000
	if page > maxPage {
		page = maxPage
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}

	// Status, when supplied, must be one of the documented enum values.
	// journal.ListRuns silently treats unknown statuses as "no filter"
	// which surfaces in the UI as "filter shows all rows" — a confusing
	// failure mode for typos.  Reject explicitly so the caller learns.
	statusRaw := r.URL.Query().Get("status")
	if statusRaw != "" && !validRunStatus(statusRaw) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "status must be one of: RUNNING, COMPLETED, FAILED, CANCELLED, TIMEOUT",
		})
		return
	}

	q := journal.RunsQuery{
		WorkspaceID: workspaceID,
		Status:      journal.RunStatus(statusRaw),
		AgentID:     r.URL.Query().Get("agent_id"),
		TriggerType: r.URL.Query().Get("trigger"),
		Tag:         r.URL.Query().Get("tag"),
		Limit:       limit,
		Offset:      offset,
	}

	aggregated, total, err := journal.ListRuns(r.Context(), h.db, q)
	if err != nil {
		h.logger.Error("list runs", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Enrich the page with agent name/slug and crew name in one query.
	// Bounded by limit (max 100), so the SQL `IN (?...)` stays small.
	runs := h.enrichRuns(r.Context(), workspaceID, aggregated)

	stats, err := journal.RunStats(r.Context(), h.db, workspaceID)
	if err != nil {
		h.logger.Error("count run stats", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, runListResponse{
		Data:  runs,
		Stats: runStats{Running: stats.Running, Today: stats.Today, Failed: stats.FailedToday},
		Pagination: pagination{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: int(math.Ceil(float64(total) / float64(limit))),
		},
	})
}

// journalCategory aliases the journal breakdown bucket so the API response
// types read locally without leaking the import into every call site.
type journalCategory = journal.CategoryCount

type runInsightTotals struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Running   int `json:"running"`
}

type runInsightDuration struct {
	P50Ms int64 `json:"p50_ms"`
	P95Ms int64 `json:"p95_ms"`
}

// crewCount is a per-crew rollup of runs; id "" / name "—" collects agents
// with no crew so those runs aren't silently dropped from the breakdown.
type crewCount struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Total  int    `json:"total"`
	Failed int    `json:"failed"`
}

// insightAgentCount is one row of the top-agents leaderboard with display names
// resolved from the agents/crews tables.
type insightAgentCount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	CrewName string `json:"crew_name"`
	Total    int    `json:"total"`
	Failed   int    `json:"failed"`
}

type runInsightsResponse struct {
	Window    string              `json:"window"`
	Totals    runInsightTotals    `json:"totals"`
	Duration  runInsightDuration  `json:"duration"`
	ByTrigger []journalCategory   `json:"by_trigger"`
	ByModel   []journalCategory   `json:"by_model"`
	ByCrew    []crewCount         `json:"by_crew"`
	TopAgents []insightAgentCount `json:"top_agents"`
	Truncated bool                `json:"truncated"`
}

// insightsTopN caps the crew rollup and agent leaderboard so the payload stays
// small regardless of workspace size — the UI shows a leaderboard, not a dump.
const insightsTopN = 8

// Insights returns the fleet operations aggregate for the workspace over a
// window (24h / 7d / 30d). Unlike Routines → Insights (routine-scoped, from
// invocation_count), this spans ALL runs — including ad-hoc agent/chat runs —
// and breaks them down by trigger, model and crew, which the routine surface
// structurally can't.
// GET /api/v1/runs/insights?window=24h
func (h *RunHandler) Insights(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}

	// Validate the window explicitly — journal.RunInsights defaults unknown
	// values to 24h, but silently widening/narrowing on a typo is a confusing
	// failure mode, so reject it at the edge like the status filter does.
	windowRaw := r.URL.Query().Get("window")
	if windowRaw == "" {
		windowRaw = "24h"
	}
	window := journal.RunInsightsWindow(windowRaw)
	switch window {
	case journal.RunWindow24h, journal.RunWindow7d, journal.RunWindow30d:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "window must be one of: 24h, 7d, 30d",
		})
		return
	}

	agg, err := journal.RunInsights(r.Context(), h.db, workspaceID, window)
	if err != nil {
		h.logger.Error("run insights", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	byCrew, topAgents := h.rollupAgents(r.Context(), workspaceID, agg.ByAgent)

	writeJSON(w, http.StatusOK, runInsightsResponse{
		Window: agg.Window,
		Totals: runInsightTotals{
			Total:     agg.Total,
			Succeeded: agg.Succeeded,
			Failed:    agg.Failed,
			Running:   agg.Running,
		},
		Duration:  runInsightDuration{P50Ms: agg.DurationP50Ms, P95Ms: agg.DurationP95Ms},
		ByTrigger: nonNilCats(agg.ByTrigger),
		ByModel:   nonNilCats(agg.ByModel),
		ByCrew:    byCrew,
		TopAgents: topAgents,
		Truncated: agg.Truncated,
	})
}

// rollupAgents resolves the journal's per-agent_id counts into a crew rollup
// and a display-named top-agents leaderboard. One workspace-scoped SELECT joins
// agents→crews (same scoping guarantee as enrichRuns). Agents whose row is
// missing (deleted, cross-workspace) keep their id and fall into the "no crew"
// bucket rather than vanishing from the totals.
func (h *RunHandler) rollupAgents(ctx context.Context, workspaceID string, byAgent []journal.AgentCount) ([]crewCount, []insightAgentCount) {
	if len(byAgent) == 0 {
		return []crewCount{}, []insightAgentCount{}
	}

	agentIDs := make([]any, 0, len(byAgent))
	for _, a := range byAgent {
		if a.AgentID != "" {
			agentIDs = append(agentIDs, a.AgentID)
		}
	}

	type meta struct {
		name, crewID, crewName string
	}
	lookup := make(map[string]meta, len(agentIDs))
	if len(agentIDs) > 0 {
		ph := "?"
		for i := 1; i < len(agentIDs); i++ {
			ph += ",?"
		}
		query := `SELECT a.id, a.name, COALESCE(a.crew_id,''), COALESCE(c.name,'')
			FROM agents a
			LEFT JOIN crews c ON c.id = a.crew_id
			WHERE a.workspace_id = ? AND a.id IN (` + ph + `)`
		args := make([]any, 0, len(agentIDs)+1)
		args = append(args, workspaceID)
		args = append(args, agentIDs...)
		rows, err := h.db.QueryContext(ctx, query, args...)
		if err == nil {
			for rows.Next() {
				var id string
				var m meta
				if err := rows.Scan(&id, &m.name, &m.crewID, &m.crewName); err == nil {
					lookup[id] = m
				}
			}
			_ = rows.Close()
		} else {
			h.logger.Warn("run insights: agent rollup lookup failed", "error", err)
		}
	}

	// Crew rollup — key on crew id, bucket crewless agents under "".
	crewAgg := map[string]*crewCount{}
	topAgents := make([]insightAgentCount, 0, len(byAgent))
	for _, a := range byAgent {
		m := lookup[a.AgentID]
		crewName := m.crewName
		if crewName == "" {
			crewName = "—"
		}
		cc := crewAgg[m.crewID]
		if cc == nil {
			cc = &crewCount{ID: m.crewID, Name: crewName}
			crewAgg[m.crewID] = cc
		}
		cc.Total += a.Total
		cc.Failed += a.Failed

		name := m.name
		if name == "" {
			name = a.AgentID
		}
		topAgents = append(topAgents, insightAgentCount{
			ID:       a.AgentID,
			Name:     name,
			CrewName: m.crewName,
			Total:    a.Total,
			Failed:   a.Failed,
		})
	}

	byCrew := make([]crewCount, 0, len(crewAgg))
	for _, c := range crewAgg {
		byCrew = append(byCrew, *c)
	}
	sort.Slice(byCrew, func(i, j int) bool {
		if byCrew[i].Total != byCrew[j].Total {
			return byCrew[i].Total > byCrew[j].Total
		}
		return byCrew[i].Name < byCrew[j].Name
	})
	// byAgent already arrives sorted by total desc from the journal layer, but
	// re-sort defensively (name resolution doesn't preserve order guarantees).
	sort.Slice(topAgents, func(i, j int) bool {
		if topAgents[i].Total != topAgents[j].Total {
			return topAgents[i].Total > topAgents[j].Total
		}
		return topAgents[i].ID < topAgents[j].ID
	})

	if len(byCrew) > insightsTopN {
		byCrew = byCrew[:insightsTopN]
	}
	if len(topAgents) > insightsTopN {
		topAgents = topAgents[:insightsTopN]
	}
	return byCrew, topAgents
}

// nonNilCats guarantees a JSON array (not null) for an empty breakdown so the
// frontend can iterate without a null guard.
func nonNilCats(c []journalCategory) []journalCategory {
	if c == nil {
		return []journalCategory{}
	}
	return c
}

// enrichRuns maps RunAggregated to runResponse and fills in agent name/slug
// and crew name with one extra SELECT keyed on the page's agent_ids.
// Errors during enrichment are logged and result in nil names — the runs
// themselves still render.
//
// The lookup is workspace-scoped so an agent_id collision across
// workspaces (test fixtures, manual SQL, restored backups) cannot
// attach a different workspace's agent/crew names to this page.
func (h *RunHandler) enrichRuns(ctx context.Context, workspaceID string, aggregated []journal.RunAggregated) []runResponse {
	if aggregated == nil {
		return []runResponse{}
	}
	// Collect unique agent_ids from the page so the lookup is bounded.
	agentIDs := make([]any, 0, len(aggregated))
	seen := map[string]struct{}{}
	for _, r := range aggregated {
		if r.AgentID == "" {
			continue
		}
		if _, ok := seen[r.AgentID]; ok {
			continue
		}
		seen[r.AgentID] = struct{}{}
		agentIDs = append(agentIDs, r.AgentID)
	}

	type lookup struct {
		name, slug, crewName sql.NullString
	}
	enriched := make(map[string]lookup, len(agentIDs))
	if len(agentIDs) > 0 {
		// Build IN (?,?,?...) placeholder list.
		ph := "?"
		for i := 1; i < len(agentIDs); i++ {
			ph += ",?"
		}
		query := `SELECT a.id, a.name, a.slug, c.name
			FROM agents a
			LEFT JOIN crews c ON c.id = a.crew_id
			WHERE a.workspace_id = ? AND a.id IN (` + ph + `)`
		args := make([]any, 0, len(agentIDs)+1)
		args = append(args, workspaceID)
		args = append(args, agentIDs...)
		rows, err := h.db.QueryContext(ctx, query, args...)
		if err == nil {
			for rows.Next() {
				var id string
				var l lookup
				if err := rows.Scan(&id, &l.name, &l.slug, &l.crewName); err == nil {
					enriched[id] = l
				}
			}
			_ = rows.Close()
		} else {
			h.logger.Warn("enrich runs lookup failed", "error", err)
		}
	}

	out := make([]runResponse, 0, len(aggregated))
	for _, r := range aggregated {
		resp := runResponse{
			ID:           r.ID,
			AgentID:      r.AgentID,
			WorkspaceID:  r.WorkspaceID,
			TriggerType:  r.TriggerType,
			Status:       string(r.Status),
			ErrorMessage: stringPtrOrNil(r.ErrorMessage),
			ExitCode:     r.ExitCode,
			Model:        stringPtrOrNil(r.Model),
			CreatedAt:    formatRFC3339(r.CreatedAt),
		}
		if r.ChatID != "" {
			c := r.ChatID
			resp.ChatID = &c
		}
		if r.TriggeredBy != "" {
			t := r.TriggeredBy
			resp.TriggeredBy = &t
		}
		if !r.StartedAt.IsZero() {
			s := formatRFC3339(r.StartedAt)
			resp.StartedAt = &s
		}
		if r.FinishedAt != nil && !r.FinishedAt.IsZero() {
			f := formatRFC3339(*r.FinishedAt)
			resp.FinishedAt = &f
		}
		if r.Metadata != nil {
			if b, err := json.Marshal(r.Metadata); err == nil {
				resp.Metadata = b
			}
		}
		if l, ok := enriched[r.AgentID]; ok {
			if l.name.Valid {
				n := l.name.String
				resp.AgentName = &n
			}
			if l.slug.Valid {
				s := l.slug.String
				resp.AgentSlug = &s
			}
			if l.crewName.Valid {
				c := l.crewName.String
				resp.CrewName = &c
			}
		}
		out = append(out, resp)
	}
	return out
}

// stringPtrOrNil returns a pointer to s when non-empty, else nil —
// matches the legacy *string convention in runResponse so the JSON
// keeps the field as null when empty rather than serialising "".
// (Distinct from nullStringPtr in crew_provisioning.go which takes a
// sql.NullString.)
func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// validRunStatus mirrors journal.RunStatus for input validation. The
// inverse — RunStatus → string — is just `string(s)`; this is the
// closed list we accept on the wire so a typo doesn't silently widen
// the result set.
func validRunStatus(s string) bool {
	switch journal.RunStatus(s) {
	case journal.RunStatusRunning, journal.RunStatusCompleted,
		journal.RunStatusFailed, journal.RunStatusCancelled,
		journal.RunStatusTimeout:
		return true
	}
	return false
}

// formatRFC3339 returns the RFC3339 string used by the legacy agent_runs
// columns. Zero time becomes empty string — caller decides whether to
// emit nil or the string.
func formatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
