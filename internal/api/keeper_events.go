package api

// Append-only keeper decision ledger (#1369).
//
// keeper_requests is written PENDING and then UPDATEd in place to its decision
// (keeper_request.go, keeper_execute.go ×2). That destroys prior state: after the
// update there is no record that the request was ever pending, how long it sat
// there, or — critically — that a decision was ever rewritten. For the load-
// bearing component of an ex-post accountability model, "the current value" is
// not an audit trail.
//
// keeper_request_events (migration v166) records every state TRANSITION as a new
// row and is append-only by DB trigger. keeper_requests stays exactly as it is —
// the current-state projection every existing reader (admin log, CLI, UI panel,
// Phase-2 filters) already depends on — so nothing has to be rewritten to read
// the current decision, while the history is now durable.
//
// TRANSACTIONALITY IS THE POINT. Every helper here performs the keeper_requests
// write and the ledger append in ONE transaction. A best-effort ledger would let
// the projection and the history diverge under load or partial failure, and a
// divergent audit trail is worse than an absent one because you cannot tell which
// half lied. The failure handling at each call site is unchanged: where the
// pre-existing code treated a failed audit write as fatal (#1021 — never decide
// without a record) it still is; where the decision had already been executed by
// the time the row was updated, the failure is still logged rather than
// pretending the side effect can be undone.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Keeper transition states. PENDING and the decision values mirror
// keeper_requests.decision — the ledger deliberately reuses that vocabulary
// instead of inventing a parallel one, so the projection and the history are
// directly comparable.
const (
	keeperStatePending = "PENDING"
)

// Actor classes for a transition. A ledger that says "ALLOW" without saying who
// caused it cannot answer the question an audit is for.
const (
	keeperActorKeeper = "keeper" // the gatekeeper evaluator decided
	keeperActorSystem = "system" // dedup suppression, timeouts, sweeps
	keeperActorAgent  = "agent"  // the requesting agent raised the request
	keeperActorUser   = "user"   // an operator resolved an escalation
)

// keeperTransition is one row of the append-only ledger. The denormalised
// request fields (agent/crew/credential/intent/command) are copied onto every
// transition on purpose: the ledger must stay readable and self-describing after
// the operational keeper_requests row is gone, and a JOIN to a table that may
// have been pruned is not an audit record.
type keeperTransition struct {
	RequestID   string
	WorkspaceID string
	// State is the state ENTERED by this transition (PENDING, ALLOW, DENY,
	// ESCALATE, DUPLICATE_SUPPRESSED, or a Phase-2 verdict).
	State        string
	RequestType  string
	AgentID      string
	CrewID       string
	CredentialID string
	Intent       string
	Command      string
	Reason       string
	// RiskScore / ExitCode are pointers so "not applicable at this transition"
	// (a PENDING has no risk score yet) is stored as NULL rather than as a
	// misleading 0.
	RiskScore *int
	ExitCode  *int
	ActorType string
	ActorID   string
}

// keeperEventInsertSQL appends the next transition for a request. The seq comes
// from a subquery in the same statement rather than a read-then-write, and
// UNIQUE(request_id, seq) turns a concurrent double-append into a loud error
// instead of a silently overwritten history.
const keeperEventInsertSQL = `
	INSERT INTO keeper_request_events
		(id, request_id, workspace_id, seq, state, request_type, requesting_agent_id,
		 requesting_crew_id, credential_id, intent, command, reason, risk_score,
		 exit_code, actor_type, actor_id, recorded_at)
	VALUES (?, ?, NULLIF(?,''),
		(SELECT COALESCE(MAX(seq), 0) + 1 FROM keeper_request_events WHERE request_id = ?),
		?, NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''), NULLIF(?,''),
		NULLIF(?,''), ?, ?, ?, NULLIF(?,''), ?)`

// appendKeeperTransitionTx appends tr to the ledger inside the caller's
// transaction. Never call this outside a transaction that also carries the
// corresponding keeper_requests write — see the file header on why the two must
// not be able to diverge.
func appendKeeperTransitionTx(ctx context.Context, tx *sql.Tx, tr keeperTransition) error {
	if tr.RequestID == "" {
		return fmt.Errorf("keeper transition: request_id is required")
	}
	actorType := tr.ActorType
	if actorType == "" {
		actorType = keeperActorKeeper
	}
	// An empty State means the caller had no decision to record — keeper_requests
	// stores that as a NULL decision, so the ledger records PENDING. Deliberately
	// NOT an error: these helpers now sit on paths that previously tolerated an
	// empty decision (a Phase-2 evaluator that returned no verdict wrote the row
	// with `nullIfEmpty(decision)` and succeeded), and turning that into a 500
	// would be an availability regression smuggled in with an audit change.
	state := tr.State
	if state == "" {
		state = keeperStatePending
	}
	var risk, exit any
	if tr.RiskScore != nil {
		risk = *tr.RiskScore
	}
	if tr.ExitCode != nil {
		exit = *tr.ExitCode
	}
	_, err := tx.ExecContext(ctx, keeperEventInsertSQL,
		generateCUID(), tr.RequestID, tr.WorkspaceID, tr.RequestID,
		state, tr.RequestType, tr.AgentID, tr.CrewID, tr.CredentialID,
		tr.Intent, tr.Command, tr.Reason, risk, exit, actorType, tr.ActorID,
		// recorded_at is indexed (workspace_id, recorded_at DESC) and read as an
		// ordering, so it goes through tsformat: a truncated fractional second is
		// not fixed-width and can sort two transitions inside one second wrongly.
		tsformat.Format(time.Now()))
	if err != nil {
		return fmt.Errorf("keeper transition: append %s for %s: %w", state, tr.RequestID, err)
	}
	return nil
}

// insertKeeperRequestWithTransition performs a keeper_requests INSERT and its
// ledger append atomically.
//
// requestSQL/requestArgs are the caller's own INSERT — kept as a parameter rather
// than reconstructed here because the three insert sites genuinely differ (access
// vs execute vs the dedup-suppressed record), and rewriting them into one
// generalised statement would be a bigger, riskier change than threading the
// transaction through.
func insertKeeperRequestWithTransition(
	ctx context.Context,
	db *sql.DB,
	requestSQL string,
	requestArgs []any,
	tr keeperTransition,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("keeper audit: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, requestSQL, requestArgs...); err != nil {
		return fmt.Errorf("keeper audit: insert request: %w", err)
	}
	if err := appendKeeperTransitionTx(ctx, tx, tr); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("keeper audit: commit: %w", err)
	}
	return nil
}

// updateKeeperDecisionWithTransition moves a keeper_requests row to its decision
// and appends the matching ledger transition atomically.
//
// decisionSQL/decisionArgs are the caller's existing UPDATE. Returning the error
// (rather than swallowing it) lets each site keep the failure handling it already
// had — the request path can surface it, the execute path logs it because the
// command has already run and the update cannot un-run it.
func updateKeeperDecisionWithTransition(
	ctx context.Context,
	db *sql.DB,
	decisionSQL string,
	decisionArgs []any,
	tr keeperTransition,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("keeper audit: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, decisionSQL, decisionArgs...); err != nil {
		return fmt.Errorf("keeper audit: update decision: %w", err)
	}
	if err := appendKeeperTransitionTx(ctx, tx, tr); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("keeper audit: commit: %w", err)
	}
	return nil
}

// keeperRequestWorkspace resolves the workspace of a keeper request from its
// requesting agent, falling back to its crew. keeper_requests has no
// workspace_id column of its own, and the ledger wants one so an admin can read
// the history per tenant without joining through a table that may since have
// been pruned.
//
// The crew fallback is load-bearing, not belt-and-braces: two Phase-2 evaluators
// (skill_review, memory_health) are crew-scoped and pass an EMPTY agent id, so
// agent-only resolution would leave their verdicts with a NULL workspace and
// therefore invisible to the workspace-scoped events endpoint.
//
// Returns "" (a NULL workspace, which the schema allows) when neither resolves.
// Silent by design: the transition itself is far more valuable than the tenant
// tag, so a missing workspace must never be the reason a decision goes
// unrecorded.
func keeperRequestWorkspace(ctx context.Context, db *sql.DB, logger *slog.Logger, agentID, crewID string) string {
	if agentID != "" {
		var ws string
		if err := db.QueryRowContext(ctx,
			`SELECT workspace_id FROM agents WHERE id = ?`, agentID).Scan(&ws); err == nil && ws != "" {
			return ws
		} else if err != nil && logger != nil {
			logger.Debug("keeper audit: could not resolve workspace from agent",
				"agent_id", agentID, "error", err)
		}
	}
	if crewID != "" {
		var ws string
		if err := db.QueryRowContext(ctx,
			`SELECT workspace_id FROM crews WHERE id = ?`, crewID).Scan(&ws); err == nil && ws != "" {
			return ws
		} else if err != nil && logger != nil {
			logger.Debug("keeper audit: could not resolve workspace from crew",
				"crew_id", crewID, "error", err)
		}
	}
	return ""
}
