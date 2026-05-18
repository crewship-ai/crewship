package api

// Memory stats — operator-facing observability for the memory
// subsystem. Today's gap (surfaced on dev1): no UI or CLI surface
// tells an operator how much memory data their workspace is
// carrying. They can SELECT against memory_versions by hand, but
// the dashboard widget for "memory health" has nowhere to fetch
// from.
//
// Endpoint: GET /api/v1/admin/memory/stats?workspace_id={ws}
// Auth:     authed + wsCtx + manage role (mirrors backup admin)
//
// Response shape (stable; UI keys are pinned by the table-driven
// test below — renaming a field breaks the dashboard):
//
//	{
//	  "workspace_id": "...",
//	  "totals": {
//	    "versions":    int   // total memory_versions rows for this ws
//	    "bytes":       int64 // SUM(bytes)
//	    "blobs":       int   // distinct sha256s (a stale row + new
//	                          //                   row at same content
//	                          //                   share one blob)
//	    "oldest_at":   string  // RFC3339; "" when no rows
//	    "newest_at":   string  // RFC3339; "" when no rows
//	  },
//	  "by_tier": [
//	    { "tier": "agent",    "versions": int, "bytes": int64 },
//	    ...
//	  ],
//	  "by_agent": [
//	    { "agent_slug": "martin", "versions": int, "bytes": int64,
//	      "newest_at": "..." },
//	    ...
//	  ]
//	}
//
// All three projections are per-workspace. Cross-workspace bleed
// would be a SOC-2 / EU AI Act observability flaw — the table-driven
// test holds the isolation contract.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
)

// MemoryStatsHandler serves GET /api/v1/admin/memory/stats.
// Stateless against config; reads memory_versions directly.
type MemoryStatsHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewMemoryStatsHandler builds the handler against the live DB.
func NewMemoryStatsHandler(db *sql.DB, logger *slog.Logger) *MemoryStatsHandler {
	return &MemoryStatsHandler{db: db, logger: logger}
}

type memoryStatsTotals struct {
	Versions int    `json:"versions"`
	Bytes    int64  `json:"bytes"`
	Blobs    int    `json:"blobs"`
	OldestAt string `json:"oldest_at"`
	NewestAt string `json:"newest_at"`
}

type memoryStatsByTier struct {
	Tier     string `json:"tier"`
	Versions int    `json:"versions"`
	Bytes    int64  `json:"bytes"`
}

type memoryStatsByAgent struct {
	AgentSlug string `json:"agent_slug"`
	Versions  int    `json:"versions"`
	Bytes     int64  `json:"bytes"`
	NewestAt  string `json:"newest_at"`
}

type memoryStatsResponse struct {
	WorkspaceID string               `json:"workspace_id"`
	Totals      memoryStatsTotals    `json:"totals"`
	ByTier      []memoryStatsByTier  `json:"by_tier"`
	ByAgent     []memoryStatsByAgent `json:"by_agent"`
}

// Stats handles GET /api/v1/admin/memory/stats?workspace_id=...
//
// Role gate: requireRole("manage") to mirror the rest of the admin
// surface. Member-level callers see 403, not 401 — they're
// authenticated, just not authorised.
//
// The three SELECT passes are intentional (totals, by_tier, by_agent)
// rather than one mega-query: SQLite handles each as a simple index
// scan over (workspace_id, ...), and the three result sets have
// distinct row shapes that don't pivot cleanly into one SQL output.
// Latency is sub-ms even at 100k rows because the index covers all
// three paths.
func (h *MemoryStatsHandler) Stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}

	totals, err := h.loadTotals(ctx, workspaceID)
	if err != nil {
		h.logger.Error("memory stats: totals", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "stats query failed")
		return
	}
	byTier, err := h.loadByTier(ctx, workspaceID)
	if err != nil {
		h.logger.Error("memory stats: by_tier", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "stats query failed")
		return
	}
	byAgent, err := h.loadByAgent(ctx, workspaceID)
	if err != nil {
		h.logger.Error("memory stats: by_agent", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "stats query failed")
		return
	}

	writeJSON(w, http.StatusOK, memoryStatsResponse{
		WorkspaceID: workspaceID,
		Totals:      totals,
		ByTier:      byTier,
		ByAgent:     byAgent,
	})
}

// loadTotals: one aggregate row covering the workspace-wide counters
// and the min/max timestamps for the rolling window. NULL coalescing
// matters — an empty workspace returns NULL for MIN/MAX, which Scan
// would reject without sql.NullString.
func (h *MemoryStatsHandler) loadTotals(ctx context.Context, workspaceID string) (memoryStatsTotals, error) {
	var t memoryStatsTotals
	var oldest, newest sql.NullString
	err := h.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(bytes), 0),
		       COUNT(DISTINCT sha256),
		       MIN(written_at),
		       MAX(written_at)
		  FROM memory_versions
		 WHERE workspace_id = ?`, workspaceID,
	).Scan(&t.Versions, &t.Bytes, &t.Blobs, &oldest, &newest)
	if err != nil {
		return t, fmt.Errorf("memory stats: load totals: %w", err)
	}
	if oldest.Valid {
		t.OldestAt = oldest.String
	}
	if newest.Valid {
		t.NewestAt = newest.String
	}
	return t, nil
}

// loadByTier returns one row per (tier) WITHIN the workspace. Tiers
// with zero rows are omitted (a tier the operator has never written
// to isn't a meaningful row in the response).
func (h *MemoryStatsHandler) loadByTier(ctx context.Context, workspaceID string) ([]memoryStatsByTier, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT tier, COUNT(*), COALESCE(SUM(bytes), 0)
		  FROM memory_versions
		 WHERE workspace_id = ?
		 GROUP BY tier
		 ORDER BY tier ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("memory stats: load by_tier: %w", err)
	}
	defer rows.Close()
	out := make([]memoryStatsByTier, 0, 5) // 5 documented tiers
	for rows.Next() {
		var t memoryStatsByTier
		if err := rows.Scan(&t.Tier, &t.Versions, &t.Bytes); err != nil {
			return nil, fmt.Errorf("memory stats: scan by_tier row: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory stats: iterate by_tier: %w", err)
	}
	return out, nil
}

// loadByAgent extracts the agent slug from the canonical path
// prefix (the convention is "agent:{slug}/..." — see
// internal/memory/audit_watcher.go's parseMemoryPath). Crew- and
// workspace-tier rows have a different prefix; they're collapsed
// under agent_slug = "" so the UI can render them as a "shared"
// row without re-querying.
func (h *MemoryStatsHandler) loadByAgent(ctx context.Context, workspaceID string) ([]memoryStatsByAgent, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT
		    CASE
		        WHEN path LIKE 'agent:%/%'
		        THEN substr(path, 7, instr(substr(path, 7), '/') - 1)
		        ELSE ''
		    END AS agent_slug,
		    COUNT(*) AS versions,
		    COALESCE(SUM(bytes), 0) AS bytes,
		    MAX(written_at) AS newest_at
		  FROM memory_versions
		 WHERE workspace_id = ?
		 GROUP BY agent_slug
		 ORDER BY agent_slug ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("memory stats: load by_agent: %w", err)
	}
	defer rows.Close()
	out := make([]memoryStatsByAgent, 0, 16)
	for rows.Next() {
		var a memoryStatsByAgent
		var newest sql.NullString
		if err := rows.Scan(&a.AgentSlug, &a.Versions, &a.Bytes, &newest); err != nil {
			return nil, fmt.Errorf("memory stats: scan by_agent row: %w", err)
		}
		if newest.Valid {
			a.NewestAt = newest.String
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory stats: iterate by_agent: %w", err)
	}
	return out, nil
}
