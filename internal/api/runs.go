package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
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
	CreatedAt    string          `json:"created_at"`
	AgentName    *string         `json:"agent_name,omitempty"`
	AgentSlug    *string         `json:"agent_slug,omitempty"`
	CrewName     *string         `json:"crew_name,omitempty"`
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
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset := (page - 1) * limit

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
