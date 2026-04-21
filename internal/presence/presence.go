// Package presence tracks agent availability — the "Watch Roster" the UI
// shows a lead before it dispatches work. Status values align with
// LangGraph-style lifecycle: online (idle-but-reachable), busy (actively
// running), blocked (waiting on keeper / approval / escalation), offline
// (not seen in >5 min). Transitions are emitted as agent.status_change
// entries into the Crew Journal so the full history replays alongside
// every other event.
package presence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Status is the snapshot value stored in agent_status. Strings match the
// CHECK constraint in migration 52 — changes here require a migration.
type Status string

const (
	StatusOnline  Status = "online"
	StatusBusy    Status = "busy"
	StatusBlocked Status = "blocked"
	StatusOffline Status = "offline"
)

// Validate rejects unknown statuses so a typo in a caller doesn't silently
// poison the roster row.
func (s Status) Validate() error {
	switch s {
	case StatusOnline, StatusBusy, StatusBlocked, StatusOffline:
		return nil
	default:
		return fmt.Errorf("presence: unknown status %q", s)
	}
}

// Snapshot is one roster row — last-write-wins view of an agent's state.
// Details carries status-specific metadata (current_task_id for busy,
// blocked_reason for blocked).
//
// MissionID is not stored in the agent_status row (it'd require a
// schema migration and a single agent can legitimately be between
// missions). It IS threaded into the agent.status_change journal
// entry so the per-mission timeline doesn't drop status transitions.
type Snapshot struct {
	AgentID     string
	WorkspaceID string
	CrewID      string
	MissionID   string
	Status      Status
	Since       time.Time
	Details     map[string]any
}

// Upsert writes the new status and emits agent.status_change IF the
// status actually changed. Same-status writes are idempotent — they
// refresh the since timestamp in memory but skip the journal emit so the
// journal stays a transition log rather than a heartbeat stream.
func Upsert(ctx context.Context, db *sql.DB, j journal.Emitter, s Snapshot) error {
	if err := s.Status.Validate(); err != nil {
		return err
	}
	if s.AgentID == "" || s.WorkspaceID == "" {
		return fmt.Errorf("presence: agent_id and workspace_id required")
	}
	if s.Since.IsZero() {
		s.Since = time.Now().UTC()
	}
	details := "{}"
	if len(s.Details) > 0 {
		if b, err := json.Marshal(s.Details); err == nil {
			details = string(b)
		}
	}

	// Read prior status first so we know whether to emit. One extra
	// query per Upsert is cheap and keeps the journal clean.
	var prev sql.NullString
	_ = db.QueryRowContext(ctx, `SELECT status FROM agent_status WHERE agent_id = ?`, s.AgentID).Scan(&prev)

	_, err := db.ExecContext(ctx, `INSERT INTO agent_status
		(agent_id, workspace_id, crew_id, status, since, details)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			crew_id      = excluded.crew_id,
			status       = excluded.status,
			since        = excluded.since,
			details      = excluded.details`,
		s.AgentID, s.WorkspaceID, nullable(s.CrewID),
		string(s.Status), s.Since.UTC().Format(time.RFC3339Nano), details)
	if err != nil {
		return fmt.Errorf("presence: upsert: %w", err)
	}

	if !prev.Valid || prev.String != string(s.Status) {
		summary := fmt.Sprintf("agent %s: %s", s.AgentID, s.Status)
		if prev.Valid {
			summary = fmt.Sprintf("agent %s: %s → %s", s.AgentID, prev.String, s.Status)
		}
		_, _ = j.Emit(ctx, journal.Entry{
			WorkspaceID: s.WorkspaceID,
			CrewID:      s.CrewID,
			AgentID:     s.AgentID,
			MissionID:   s.MissionID,
			Type:        journal.EntryAgentStatus,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorSystem,
			Summary:     summary,
			Payload: map[string]any{
				"status":  string(s.Status),
				"prev":    prev.String,
				"details": s.Details,
			},
		})
	}
	return nil
}

// Get returns the last-known snapshot for an agent or nil if none.
func Get(ctx context.Context, db *sql.DB, agentID string) (*Snapshot, error) {
	row := db.QueryRowContext(ctx, `SELECT agent_id, workspace_id, crew_id, status, since, details
		FROM agent_status WHERE agent_id = ?`, agentID)
	var s Snapshot
	var crewID sql.NullString
	var sinceStr, detailsStr, statusStr string
	if err := row.Scan(&s.AgentID, &s.WorkspaceID, &crewID, &statusStr, &sinceStr, &detailsStr); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	s.CrewID = crewID.String
	s.Status = Status(statusStr)
	if t, err := time.Parse(time.RFC3339Nano, sinceStr); err == nil {
		s.Since = t
	}
	if detailsStr != "" && detailsStr != "{}" {
		_ = json.Unmarshal([]byte(detailsStr), &s.Details)
	}
	return &s, nil
}

// ListByCrew returns every agent snapshot in a crew, ordered newest-
// activity-first. The lead UI uses this for the Watch Roster.
func ListByCrew(ctx context.Context, db *sql.DB, crewID string) ([]Snapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT agent_id, workspace_id, crew_id, status, since, details
		FROM agent_status WHERE crew_id = ? ORDER BY since DESC`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSnapshots(rows)
}

// ListByWorkspace returns every agent snapshot in a workspace.
func ListByWorkspace(ctx context.Context, db *sql.DB, workspaceID string) ([]Snapshot, error) {
	rows, err := db.QueryContext(ctx, `SELECT agent_id, workspace_id, crew_id, status, since, details
		FROM agent_status WHERE workspace_id = ? ORDER BY since DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSnapshots(rows)
}

// SweepOffline flips agents whose last activity is older than threshold
// to offline. Called by a 60s background tick; threshold defaults to
// 5 minutes. Emits one agent.status_change per transition so the journal
// shows the timeout, not just silent disappearance.
func SweepOffline(ctx context.Context, db *sql.DB, j journal.Emitter, threshold time.Duration) error {
	if threshold <= 0 {
		threshold = 5 * time.Minute
	}
	cutoff := time.Now().UTC().Add(-threshold).Format(time.RFC3339Nano)
	rows, err := db.QueryContext(ctx, `SELECT agent_id, workspace_id, crew_id
		FROM agent_status WHERE status != 'offline' AND since < ?`, cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()
	type stale struct{ agentID, workspaceID, crewID string }
	var pending []stale
	for rows.Next() {
		var s stale
		var crewID sql.NullString
		if err := rows.Scan(&s.agentID, &s.workspaceID, &crewID); err != nil {
			continue
		}
		s.crewID = crewID.String
		pending = append(pending, s)
	}
	_ = rows.Close()

	for _, s := range pending {
		_ = Upsert(ctx, db, j, Snapshot{
			AgentID:     s.agentID,
			WorkspaceID: s.workspaceID,
			CrewID:      s.crewID,
			Status:      StatusOffline,
			Details:     map[string]any{"reason": "idle_timeout"},
		})
	}
	return nil
}

func scanSnapshots(rows *sql.Rows) ([]Snapshot, error) {
	var out []Snapshot
	for rows.Next() {
		var s Snapshot
		var crewID sql.NullString
		var sinceStr, detailsStr, statusStr string
		if err := rows.Scan(&s.AgentID, &s.WorkspaceID, &crewID, &statusStr, &sinceStr, &detailsStr); err != nil {
			return nil, err
		}
		s.CrewID = crewID.String
		s.Status = Status(statusStr)
		if t, err := time.Parse(time.RFC3339Nano, sinceStr); err == nil {
			s.Since = t
		}
		if detailsStr != "" && detailsStr != "{}" {
			_ = json.Unmarshal([]byte(detailsStr), &s.Details)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
