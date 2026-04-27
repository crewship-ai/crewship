package paymaster

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CrewSpend is one row of "how much each crew in a workspace spent". CallCount
// is included alongside cost because the UI surfaces both — a high cost with
// few calls flags expensive prompts; many cheap calls flags chatter.
// JSON field names use snake_case to match the API contract the CLI and
// frontend both expect. Without tags Go default-marshals to CamelCase
// (CrewID) and downstream consumers silently get zero values when they
// try to read crew_id — this bit us end-to-end during the dev smoke
// test (paymaster showed $0 despite a populated cost_ledger).
type CrewSpend struct {
	CrewID    string  `json:"crew_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int64   `json:"call_count"`
	InTokens  int64   `json:"input_tokens"`
	OutTokens int64   `json:"output_tokens"`
}

// AgentSpend is the per-agent rollup within one crew. Same shape as CrewSpend
// but keyed by agent. CrewID is implicit (it's the parameter to the query).
type AgentSpend struct {
	AgentID   string  `json:"agent_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int64   `json:"call_count"`
	InTokens  int64   `json:"input_tokens"`
	OutTokens int64   `json:"output_tokens"`
}

// MissionSpend rolls up everything spent against a mission, regardless of
// which crew or agent did the spending. Useful for "what did this campaign
// cost us in total".
type MissionSpend struct {
	MissionID string    `json:"mission_id"`
	CostUSD   float64   `json:"cost_usd"`
	CallCount int64     `json:"call_count"`
	InTokens  int64     `json:"input_tokens"`
	OutTokens int64     `json:"output_tokens"`
	FirstTS   time.Time `json:"first_ts"`
	LastTS    time.Time `json:"last_ts"`
}

// TopSpender is one row of the leaderboard. Kind is "crew" or "agent" so the
// same query can serve both views; the UI picks based on what it wants. ID
// is the crew_id or agent_id depending on Kind.
type TopSpender struct {
	Kind      string  `json:"scope_kind"`
	ID        string  `json:"scope_id"`
	CostUSD   float64 `json:"cost_usd"`
	CallCount int64   `json:"call_count"`
}

// SpendByCrew aggregates ledger rows in the workspace into one row per crew
// for the given window. Rows with NULL crew_id (workspace-level spend that
// wasn't attributed to a crew) are returned with CrewID="" so the caller can
// surface them as "unattributed" rather than silently dropping them.
//
// since/until are inclusive lower / exclusive upper bounds. Pass zero time
// for either to disable that side of the range — until=zero means "up to now".
func SpendByCrew(ctx context.Context, db *sql.DB, workspaceID string, since, until time.Time) ([]CrewSpend, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("paymaster: workspace_id required")
	}
	q, args := buildAggregateQuery(
		`SELECT COALESCE(crew_id, ''), SUM(cost_usd), COUNT(*),
		        SUM(input_tokens), SUM(output_tokens)
		 FROM cost_ledger`,
		`GROUP BY crew_id ORDER BY SUM(cost_usd) DESC`,
		workspaceID, "", "", "", since, until,
	)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("paymaster: query crew spend: %w", err)
	}
	defer rows.Close()

	var out []CrewSpend
	for rows.Next() {
		var s CrewSpend
		if err := rows.Scan(&s.CrewID, &s.CostUSD, &s.CallCount,
			&s.InTokens, &s.OutTokens); err != nil {
			return nil, fmt.Errorf("paymaster: scan crew spend: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SpendByAgent is the per-agent rollup within a crew. Same semantics as
// SpendByCrew (NULL agent_ids surface as ""). crewID is required because
// agent IDs are not unique across crews in some legacy data.
func SpendByAgent(ctx context.Context, db *sql.DB, crewID string, since, until time.Time) ([]AgentSpend, error) {
	if crewID == "" {
		return nil, fmt.Errorf("paymaster: crew_id required")
	}
	q, args := buildAggregateQuery(
		`SELECT COALESCE(agent_id, ''), SUM(cost_usd), COUNT(*),
		        SUM(input_tokens), SUM(output_tokens)
		 FROM cost_ledger`,
		`GROUP BY agent_id ORDER BY SUM(cost_usd) DESC`,
		"", crewID, "", "", since, until,
	)
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("paymaster: query agent spend: %w", err)
	}
	defer rows.Close()

	var out []AgentSpend
	for rows.Next() {
		var s AgentSpend
		if err := rows.Scan(&s.AgentID, &s.CostUSD, &s.CallCount,
			&s.InTokens, &s.OutTokens); err != nil {
			return nil, fmt.Errorf("paymaster: scan agent spend: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SpendByMission is the single-mission rollup. Returns one row (FirstTS/LastTS
// span the activity window) or zero rows if no spend has been recorded.
// since/until are not exposed because mission cost is window-less by
// convention — we want the full life of the mission.
func SpendByMission(ctx context.Context, db *sql.DB, missionID string) (MissionSpend, error) {
	if missionID == "" {
		return MissionSpend{}, fmt.Errorf("paymaster: mission_id required")
	}
	const q = `SELECT COALESCE(SUM(cost_usd), 0), COUNT(*),
	                  COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
	                  COALESCE(MIN(ts), ''), COALESCE(MAX(ts), '')
	           FROM cost_ledger WHERE mission_id = ?`
	var (
		s        MissionSpend
		firstStr string
		lastStr  string
	)
	s.MissionID = missionID
	if err := db.QueryRowContext(ctx, q, missionID).Scan(
		&s.CostUSD, &s.CallCount, &s.InTokens, &s.OutTokens, &firstStr, &lastStr,
	); err != nil {
		return MissionSpend{}, fmt.Errorf("paymaster: query mission spend: %w", err)
	}
	if firstStr != "" {
		if t, err := time.Parse(tsLayout, firstStr); err == nil {
			s.FirstTS = t
		}
	}
	if lastStr != "" {
		if t, err := time.Parse(tsLayout, lastStr); err == nil {
			s.LastTS = t
		}
	}
	return s, nil
}

// TopSpenders returns the top-N agents ordered by cost in the workspace since
// `since`. Used by the leaderboard widget on the workspace dashboard. limit
// is clamped to [1, 100] so a misconfigured caller can't drag in megabytes
// of rows.
func TopSpenders(ctx context.Context, db *sql.DB, workspaceID string, limit int, since time.Time) ([]TopSpender, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("paymaster: workspace_id required")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	conds := "workspace_id = ? AND agent_id IS NOT NULL"
	args := []any{workspaceID}
	if !since.IsZero() {
		conds += " AND ts >= ?"
		args = append(args, since.UTC().Format(tsLayout))
	}

	q := fmt.Sprintf(`
SELECT 'agent' AS kind, agent_id, SUM(cost_usd) AS cost, COUNT(*) AS calls
FROM cost_ledger
WHERE %s
GROUP BY agent_id
ORDER BY cost DESC
LIMIT %d`, conds, limit)

	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("paymaster: query top spenders: %w", err)
	}
	defer rows.Close()

	var out []TopSpender
	for rows.Next() {
		var s TopSpender
		if err := rows.Scan(&s.Kind, &s.ID, &s.CostUSD, &s.CallCount); err != nil {
			return nil, fmt.Errorf("paymaster: scan top spender: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// buildAggregateQuery composes the SELECT-prefix + WHERE + GROUP BY suffix
// for the per-{crew,agent} rollups. Centralising the WHERE construction means
// the time-range parsing only lives in one place and stays consistent.
//
// Exactly one of workspaceID / crewID / agentID / missionID is expected to be
// non-empty; the others are passed as "" and filtered out. since/until are
// optional and skipped when zero.
func buildAggregateQuery(selectPart, suffix, workspaceID, crewID, agentID, missionID string, since, until time.Time) (string, []any) {
	conds := []string{}
	args := []any{}

	if workspaceID != "" {
		conds = append(conds, "workspace_id = ?")
		args = append(args, workspaceID)
	}
	if crewID != "" {
		conds = append(conds, "crew_id = ?")
		args = append(args, crewID)
	}
	if agentID != "" {
		conds = append(conds, "agent_id = ?")
		args = append(args, agentID)
	}
	if missionID != "" {
		conds = append(conds, "mission_id = ?")
		args = append(args, missionID)
	}
	if !since.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, since.UTC().Format(tsLayout))
	}
	if !until.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, until.UTC().Format(tsLayout))
	}

	q := selectPart
	if len(conds) > 0 {
		q += " WHERE " + joinAnd(conds)
	}
	q += " " + suffix
	return q, args
}
