package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// HealthSnapshot is the 5-metric score Auto-Dream popularised,
// adapted to Crewship's journal + embeddings model. Every metric is
// in [0, 100]; Overall is a weighted average using the Auto-Dream
// weights that passed community scrutiny (Freshness 25 / Coverage 25
// / Coherence 20 / Efficiency 15 / Reachability 15).
type HealthSnapshot struct {
	WorkspaceID  string
	CrewID       string
	ComputedAt   time.Time
	Freshness    float64
	Coverage     float64
	Coherence    float64
	Efficiency   float64
	Reachability float64
	Overall      float64
	Details      map[string]any
}

// ComputeHealth builds a snapshot for one (workspace, crew). crewID
// may be empty to compute a workspace-wide view. The function only
// READs from the DB and is cheap enough to run on every API call;
// production installs call it daily to persist to
// memory_health_snapshots, but UIs can call it live for a
// just-in-time number.
func ComputeHealth(ctx context.Context, db *sql.DB, workspaceID, crewID string) (HealthSnapshot, error) {
	if workspaceID == "" {
		return HealthSnapshot{}, fmt.Errorf("health: workspace_id required")
	}
	s := HealthSnapshot{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		ComputedAt:  time.Now().UTC(),
		Details:     map[string]any{},
	}

	// Common scope predicate reused across every metric query. Empty
	// crew => workspace-wide; specific crew => crew-scoped. Keeping
	// this in one spot means a single bug fix covers every metric.
	scopeSQL, scopeArgs := scopeClause(workspaceID, crewID, "e")

	// ---- Freshness (25%) -------------------------------------------
	// Recent activity as a fraction of rolling baseline. Today vs
	// 7-day average. 100 when today matches or beats the baseline,
	// scaled down linearly when recent volume drops. Captures
	// "memory is going stale because the crew stopped writing".
	var recent, baseline float64
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries e WHERE `+scopeSQL+` AND e.ts >= datetime('now','-24 hours')`,
		scopeArgs...).Scan(&recent)
	_ = db.QueryRowContext(ctx,
		`SELECT CAST(COUNT(*) AS REAL) / 7.0 FROM journal_entries e WHERE `+scopeSQL+` AND e.ts >= datetime('now','-7 days')`,
		scopeArgs...).Scan(&baseline)
	switch {
	case baseline == 0:
		s.Freshness = 100 // no history = nothing to compare, don't punish
	case recent >= baseline:
		s.Freshness = 100
	default:
		s.Freshness = 100 * recent / baseline
	}
	s.Details["freshness_recent_24h"] = recent
	s.Details["freshness_baseline_daily"] = baseline

	// ---- Coverage (25%) --------------------------------------------
	// How many distinct entry_type buckets have been written in the
	// last 30 days. Captures "is the crew producing varied events or
	// just running the same loop". Max is the embeddable allowlist
	// size (8) — we don't expect every workspace to light up all of
	// them so full coverage isn't the target; 50% is healthy.
	var distinctTypes float64
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT entry_type) FROM journal_entries e
		  WHERE `+scopeSQL+` AND e.ts >= datetime('now','-30 days')`,
		scopeArgs...).Scan(&distinctTypes)
	const totalEmbeddable = 8.0 // EntryPeerEscalation, PeerConversation, SummaryGenerated, MemoryConsolidated, ApprovalDenied, EvalRegression, KeeperDecision, MissionStatus
	covRatio := distinctTypes / totalEmbeddable
	if covRatio > 1 {
		covRatio = 1
	}
	s.Coverage = 100 * covRatio
	s.Details["coverage_distinct_types"] = distinctTypes

	// ---- Coherence (20%) -------------------------------------------
	// Average relations-per-embedding. High coherence means recent
	// memories connect to older ones (shared vocabulary, semantic
	// continuity). Capped at 1.0 ratio = 100% — when every
	// embedding has at least one similar neighbour we call it fully
	// coherent.
	var relCount, embCount float64
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_relations r
		   JOIN journal_entries e ON e.id = r.entry_id
		  WHERE `+scopeSQL+` AND r.relation_kind = 'similar'`,
		scopeArgs...).Scan(&relCount)
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_embeddings em
		   JOIN journal_entries e ON e.id = em.entry_id
		  WHERE `+scopeSQL+` AND em.dim > 0`,
		scopeArgs...).Scan(&embCount)
	coherence := 0.0
	if embCount > 0 {
		coherence = relCount / embCount
		if coherence > 1 {
			coherence = 1
		}
	}
	s.Coherence = 100 * coherence
	s.Details["coherence_relations"] = relCount
	s.Details["coherence_embeddings"] = embCount

	// ---- Efficiency (15%) ------------------------------------------
	// Archived vs live ratio. Older low-value bulk should be archived
	// (and live storage lean). 50% archived is the target; higher is
	// fine, lower suggests compactor isn't running often enough.
	var archived, live float64
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries_archived e WHERE `+scopeSQL,
		scopeArgs...).Scan(&archived)
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries e WHERE `+scopeSQL,
		scopeArgs...).Scan(&live)
	efficiency := 100.0
	if live+archived > 0 {
		// Saturating — reaching 50% archived yields full 100 score.
		ratio := archived / (live + archived)
		if ratio > 0.5 {
			ratio = 0.5
		}
		efficiency = 100 * (ratio / 0.5)
	}
	s.Efficiency = efficiency
	s.Details["efficiency_archived"] = archived
	s.Details["efficiency_live"] = live

	// ---- Reachability (15%) ----------------------------------------
	// What fraction of embedded entries are in the connected
	// component of the relation graph. A hub-and-spoke memory (every
	// entry connected to one popular entry) scores near 100; a
	// balkanised memory (many disconnected clusters) scores lower.
	var connected float64
	_ = db.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT e.id) FROM journal_entries e
		   JOIN journal_embeddings em ON em.entry_id = e.id AND em.dim > 0
		  WHERE `+scopeSQL+`
		    AND (EXISTS (SELECT 1 FROM memory_relations r WHERE r.entry_id = e.id)
		      OR EXISTS (SELECT 1 FROM memory_relations r WHERE r.related_entry_id = e.id))`,
		scopeArgs...).Scan(&connected)
	reach := 0.0
	if embCount > 0 {
		reach = connected / embCount
	}
	s.Reachability = 100 * reach
	s.Details["reachability_connected"] = connected

	// ---- Weighted overall -------------------------------------------
	s.Overall = 0.25*s.Freshness + 0.25*s.Coverage + 0.20*s.Coherence + 0.15*s.Efficiency + 0.15*s.Reachability

	return s, nil
}

// scopeClause returns a parameterised WHERE fragment + its args for a
// (workspace, crew) scope on any table aliased to `alias` that has
// workspace_id + crew_id columns. Centralised so every metric uses the
// same scoping rules — changing one rule doesn't risk one metric
// drifting from the others.
func scopeClause(workspaceID, crewID, alias string) (string, []any) {
	if crewID == "" {
		return alias + ".workspace_id = ?", []any{workspaceID}
	}
	return alias + ".workspace_id = ? AND " + alias + ".crew_id = ?", []any{workspaceID, crewID}
}

// PersistSnapshot writes the computed snapshot to
// memory_health_snapshots so the UI has a monotonic time series
// without recomputing 5 queries on every page load. Called from the
// daily compaction tick.
func PersistSnapshot(ctx context.Context, db *sql.DB, s HealthSnapshot) error {
	detailsJSON, err := json.Marshal(s.Details)
	if err != nil {
		return fmt.Errorf("health: marshal details: %w", err)
	}
	id := "hs_" + randomHex(12)
	_, err = db.ExecContext(ctx,
		`INSERT INTO memory_health_snapshots
		 (id, workspace_id, crew_id, computed_at, freshness, coverage, coherence, efficiency, reachability, overall, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, s.WorkspaceID, nullableHealthCrew(s.CrewID),
		s.ComputedAt.UTC().Format(time.RFC3339Nano),
		s.Freshness, s.Coverage, s.Coherence, s.Efficiency, s.Reachability, s.Overall,
		string(detailsJSON))
	if err != nil {
		return fmt.Errorf("health: persist: %w", err)
	}
	return nil
}

func nullableHealthCrew(c string) any {
	if c == "" {
		return nil
	}
	return c
}

// randomHex is a small local helper so health.go doesn't depend on
// the harbormaster token generator. 12 hex chars is plenty for an
// ID that's unique within a single workspace's snapshots.
func randomHex(n int) string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, n)
	now := time.Now().UnixNano()
	for i := range b {
		b[i] = hexdigits[int(now>>(i*4))&0xf]
	}
	return string(b)
}
