package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// DeciderKind names WHO is trying to resolve a waitpoint. It is the one
// fact the resolve door checks before anything else (PRD §18 scenario 10,
// B14 #2388): a waitpoint is a question for a person, and the answer has
// to come from one.
type DeciderKind string

const (
	// DeciderUser is a workspace member acting through a session or a CLI
	// token — the inbox, the routine page, `crewship routine waitpoints
	// approve`. The only kind that carries a user id.
	DeciderUser DeciderKind = "user"
	// DeciderExternal is a holder of the waitpoint token itself, arriving
	// on the public callback (`POST /api/v1/waitpoint-tokens/{token}`) —
	// a CI job, an approval service, a vendor webhook. The token is the
	// credential; the decision is attributed to the "external-callback"
	// sentinel.
	DeciderExternal DeciderKind = "external"
	// DeciderAgent is an agent: a crew-bound X-Internal-Token, a sidecar
	// tool, a peer posting "GO". Never allowed to decide — however it
	// arrives, the door refuses it and records that it tried.
	DeciderAgent DeciderKind = "agent"
)

// WaitpointDecider is the actor behind a decision. Kind is checked by
// Decide; ID is what lands in pipeline_waitpoints.decided_by_user_id (a
// user id, or the external sentinel) or, for a refused actor, in the
// refusal record.
type WaitpointDecider struct {
	Kind DeciderKind
	ID   string
}

// ErrDeciderNotAllowed is returned by Decide when the actor is not one a
// waitpoint accepts an answer from — an agent, or an unidentified caller.
// The waitpoint is left exactly as it was; the attempt is recorded.
var ErrDeciderNotAllowed = errors.New("waitpoint: only a person or the token's external holder can decide it; an agent cannot")

// AuditActionWaitpointDecisionRefused is the audit_logs.action under which
// a refused decision attempt is recorded. `crewship audit --action
// waitpoint.decision_refused` lists them.
const AuditActionWaitpointDecisionRefused = "waitpoint.decision_refused"

// Decide is THE resolve door for a pending approval waitpoint: every path
// that turns a pending row into approved/denied goes through here — the
// authed approve endpoint, the public token callback, and any agent-facing
// route someone wires in the future. It refuses before it touches the row
// when the decider is not a person or the external token holder, and
// records the refusal so "did anyone try to talk this gate open" is
// answerable from the audit log.
//
// An empty Kind is refused too. A caller that cannot say who it is acting
// for has not earned the decision; failing closed here is what keeps a
// missing context value from silently becoming an approval.
func (s *SQLWaitpointStore) Decide(ctx context.Context, workspaceID, token string, approved bool, decider WaitpointDecider, payload string) error {
	switch decider.Kind {
	case DeciderUser, DeciderExternal:
		return s.CompleteApproval(ctx, workspaceID, token, approved, decider.ID, payload)
	default:
		s.recordRefusal(ctx, workspaceID, token, approved, decider)
		kind := string(decider.Kind)
		if kind == "" {
			kind = "unidentified"
		}
		return fmt.Errorf("%w (actor kind %q)", ErrDeciderNotAllowed, kind)
	}
}

// recordRefusal writes the refused attempt to audit_logs. Best effort: a
// failed audit write is logged and does not turn a refusal into anything
// else. The row has no user_id (the actor is not a user) and names the
// actor in metadata; the waitpoint's own status is read back so the record
// states what the attempt left untouched.
func (s *SQLWaitpointStore) recordRefusal(ctx context.Context, workspaceID, token string, approved bool, decider WaitpointDecider) {
	status, runID := "", ""
	_ = s.db.QueryRowContext(ctx,
		`SELECT status, pipeline_run_id FROM pipeline_waitpoints WHERE token = ? AND workspace_id = ?`,
		token, workspaceID).Scan(&status, &runID)
	if workspaceID == "" {
		// The public callback resolves the token's own workspace before it
		// reaches the door; a refusal with no workspace cannot satisfy the
		// audit_logs FK, so log it and stop.
		slog.Default().Warn("waitpoint decision refused (no workspace to record it under)",
			"token", token, "actor_kind", decider.Kind, "actor_id", decider.ID)
		return
	}
	meta, _ := json.Marshal(map[string]any{
		"actor_kind":       string(decider.Kind),
		"actor_id":         decider.ID,
		"attempted":        map[bool]string{true: "approve", false: "deny"}[approved],
		"waitpoint_status": status,
		"pipeline_run_id":  runID,
		"reason":           "only a person or the token's external holder can decide a waitpoint",
	})
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_logs (id, workspace_id, user_id, action, entity_type, entity_id, metadata, created_at)
		VALUES (lower(hex(randomblob(16))), ?, NULL, ?, 'waitpoint', ?, ?, ?)`,
		workspaceID, AuditActionWaitpointDecisionRefused, token, string(meta), now); err != nil {
		slog.Default().Warn("waitpoint decision refusal audit write failed", "error", err, "token", token)
	}
}
