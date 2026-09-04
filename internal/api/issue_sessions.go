package api

// issue_sessions.go — the minimal issue_agent_sessions read/write path B1
// asks for (PRD-ISSUES-AND-ROUTINES-2026 §9.2, work package B1, #2332).
//
// Two things live here:
//
//   - resolveOrCreateIssueAgentSession, the write side. Called from
//     DispatchMention (issue_mentions.go) so the B1 accept line ("a mention
//     reuses an existing session rather than creating a second") is proven at
//     the one entry point B1 wires it into. The state machine transitions
//     themselves (§10.1: pending -> active on a claimed run, -> idle on run
//     end, the lease/idle sweeps into error/stale) are NOT this package's
//     job — B1 creates a session in 'pending' and stops there; B2/B4 move it.
//   - ListSessions, the read side — GET .../issues/{identifier}/sessions,
//     the CLI's `issue sessions` command reads this. Mirrors ListRuns'
//     shape (issue_handler_runs.go): resolve the issue, scope to its
//     workspace, one query, newest-first.
//
// What is deliberately NOT here: delivery/wake bookkeeping (§9.3, B2, lives
// in issue_deliveries.go) and the state machine transitions of §10.1 beyond
// 'pending'. The partial unique index that makes assignments.session_id an
// exclusivity key (§9.4, B3 — idx_assignments_one_active_per_session,
// 20260904172200_assignments_one_active_per_session.sql) DOES reach into
// this file as of B3: resolveOrCreateIssueAgentSessionTx below is the half
// of the insert-path rewrite that runs inside DispatchMention's transaction
// alongside insertCappedAssignment (delegation_limits.go) — see that
// function's doc comment for the other half.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/featureflags"
)

// issueAgentSessionsFlagKey gates resolveOrCreateIssueAgentSession — see
// 20260904095704_issue_agent_sessions_flag.sql for why session creation
// specifically (and not the mission_activity widening, which every producer
// moves onto unconditionally) gets a kill switch.
const issueAgentSessionsFlagKey = "issue_agent_sessions"

// dbConn is the subset of *sql.DB / *sql.Tx the session-resolve and
// assignment-insert write path shares. *sql.DB satisfies it for every
// caller that has no reason to hold a transaction open; DispatchMention
// (issue_mentions.go, B3 — §9.4) passes a *sql.Tx instead, so
// resolveOrCreateIssueAgentSessionTx and insertCappedAssignment run inside
// the SAME transaction as the fan-out guard — the thing §9.4 says the
// exclusivity index "guards nothing" without. Modelled on auditExecer
// (credential_audit.go), the same split for the same reason.
type dbConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// resolveOrCreateIssueAgentSession returns the id of the (mission, agent)
// session, creating it in state 'pending' if none exists yet.
//
// This is the flag-checked, no-transaction convenience form: it reads
// issueAgentSessionsFlagKey (which needs a concrete *sql.DB —
// featureflags.IsEnabled has no *sql.Tx overload) and, if enabled, delegates
// the actual write to resolveOrCreateIssueAgentSessionTx. Kept for any
// caller that has no reason to hold a transaction open across the call;
// DispatchMention is not that caller as of B3 — see this function's sibling.
//
// Returns ("", nil) when the flag is off: the caller treats that exactly
// like "no session" and simply does not set assignments.session_id, which
// is the column's own default.
func resolveOrCreateIssueAgentSession(ctx context.Context, db *sql.DB, workspaceID, missionID, agentID string) (string, error) {
	enabled, err := featureflags.IsEnabled(ctx, db, workspaceID, issueAgentSessionsFlagKey)
	if err != nil {
		return "", err
	}
	if !enabled {
		return "", nil
	}
	return resolveOrCreateIssueAgentSessionTx(ctx, db, workspaceID, missionID, agentID)
}

// resolveOrCreateIssueAgentSessionTx is resolveOrCreateIssueAgentSession's
// write half, with the flag check split out so DispatchMention (B3) can run
// it inside the SAME transaction as insertCappedAssignment's fan-out guard.
// Callers are responsible for the flag check (see resolveOrCreateIssueAgentSession)
// and for the transaction's lifetime — this function neither begins nor
// commits/rolls one back.
//
// One UPSERT against UNIQUE(mission_id, agent_id)
// (20260904095702_issue_agent_sessions.sql), not a SELECT-then-INSERT: two
// mentions of the same agent racing on the same issue must still produce
// exactly one row, and an UPSERT is atomic under SQLite's single-writer
// model the same way missionactivity.Emit's seq allocation is (both rely on
// database.Open's `_txlock=immediate`, though this statement does not even
// need an explicit transaction — it is one statement).
//
// agent_version is stamped from the agent's current agent_config_history
// high-water mark (§11.6) ONLY on the INSERT branch — an existing session
// keeps whatever version it was opened under; re-stamping on every reuse
// would defeat the point of pinning it.
func resolveOrCreateIssueAgentSessionTx(ctx context.Context, conn dbConn, workspaceID, missionID, agentID string) (string, error) {
	var agentVersion sql.NullInt64
	if err := conn.QueryRowContext(ctx,
		`SELECT MAX(version) FROM agent_config_history WHERE agent_id = ?`, agentID,
	).Scan(&agentVersion); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	newID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := conn.ExecContext(ctx, `
		INSERT INTO issue_agent_sessions
		    (id, workspace_id, mission_id, agent_id, state, last_consumed_seq,
		     agent_version, last_activity_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', 0, ?, ?, ?, ?)
		ON CONFLICT(mission_id, agent_id) DO UPDATE SET updated_at = excluded.updated_at`,
		newID, workspaceID, missionID, agentID, nullableInt64(agentVersion), now, now, now)
	if err != nil {
		return "", err
	}

	var sessionID string
	if err := conn.QueryRowContext(ctx,
		`SELECT id FROM issue_agent_sessions WHERE mission_id = ? AND agent_id = ?`,
		missionID, agentID,
	).Scan(&sessionID); err != nil {
		return "", err
	}
	return sessionID, nil
}

func nullableInt64(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}

// issueAgentSessionDTO is one row of the issue's session panel — every
// column §9.2 names, JSON-shaped for the CLI and the (future) dashboard
// panel to share.
type issueAgentSessionDTO struct {
	ID              string `json:"id"`
	MissionID       string `json:"mission_id"`
	AgentID         string `json:"agent_id"`
	AgentName       string `json:"agent_name,omitempty"`
	State           string `json:"state"`
	LastConsumedSeq int    `json:"last_consumed_seq"`
	ActiveRunID     string `json:"active_run_id,omitempty"`
	AgentVersion    *int   `json:"agent_version,omitempty"`
	LastActivityAt  string `json:"last_activity_at,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// ListSessions GET /api/v1/crews/{crewId}/issues/{identifier}/sessions
//
// Every issue_agent_sessions row for this issue, newest-first. Mirrors
// ListRuns' shape (issue_handler_runs.go) — resolve the issue, scope to
// its workspace, one query.
func (h *IssueHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "read") {
		return
	}
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "issue sessions: resolve mission", err)
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT s.id, s.mission_id, s.agent_id, COALESCE(a.name, ''), s.state,
		       s.last_consumed_seq, COALESCE(s.active_run_id, ''), s.agent_version,
		       COALESCE(s.last_activity_at, ''), s.created_at, s.updated_at
		FROM issue_agent_sessions s
		LEFT JOIN agents a ON a.id = s.agent_id
		WHERE s.mission_id = ? AND s.workspace_id = ?
		ORDER BY s.updated_at DESC`, missionID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "issue sessions: query", err)
		return
	}
	defer rows.Close()

	out := []issueAgentSessionDTO{}
	for rows.Next() {
		var dto issueAgentSessionDTO
		var agentVersion sql.NullInt64
		if err := rows.Scan(&dto.ID, &dto.MissionID, &dto.AgentID, &dto.AgentName, &dto.State,
			&dto.LastConsumedSeq, &dto.ActiveRunID, &agentVersion,
			&dto.LastActivityAt, &dto.CreatedAt, &dto.UpdatedAt); err != nil {
			internalError(w, r, h.logger, "issue sessions: scan", err)
			return
		}
		if agentVersion.Valid {
			v := int(agentVersion.Int64)
			dto.AgentVersion = &v
		}
		out = append(out, dto)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "issue sessions: rows", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
