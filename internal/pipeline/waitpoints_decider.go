package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
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
	if err := s.checkHeldActionDecider(ctx, workspaceID, token, approved, decider); err != nil {
		return err
	}
	switch decider.Kind {
	case DeciderUser:
		return s.CompleteApproval(ctx, workspaceID, token, approved, decider.ID, payload)
	case DeciderExternal:
		var form string
		err := s.db.QueryRowContext(ctx, `SELECT decision_form_json FROM pipeline_waitpoints WHERE workspace_id=? AND token=?`, workspaceID, token).Scan(&form)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAlreadyDecided
		}
		if err != nil {
			return fmt.Errorf("waitpoints: check decision audience: %w", err)
		}
		if form != "" {
			s.recordRefusal(ctx, workspaceID, token, approved, decider, refusalReasonDecider)
			return fmt.Errorf("%w: typed decisions require an authenticated workspace user", ErrDeciderNotAllowed)
		}
		return s.CompleteApproval(ctx, workspaceID, token, approved, decider.ID, payload)
	default:
		s.recordRefusal(ctx, workspaceID, token, approved, decider, refusalReasonDecider)
		kind := string(decider.Kind)
		if kind == "" {
			kind = "unidentified"
		}
		return fmt.Errorf("%w (actor kind %q)", ErrDeciderNotAllowed, kind)
	}
}

const (
	refusalReasonDecider    = "only a person or the token's external holder can decide a waitpoint"
	refusalReasonHeldAction = "a held routine action is decided by a workspace OWNER or ADMIN, never by the token's holder"
)

// checkHeldActionDecider applies the held-action rule before any other:
// a waitpoint that holds a crewship action for approval
// (IsCrewshipApprovalStepKey) is decided only by a person whose CURRENT
// workspace role is OWNER or ADMIN. The token's external holder, a member
// of a lower role and an agent are refused, and the attempt is recorded.
// Any other waitpoint passes through unchanged.
func (s *SQLWaitpointStore) checkHeldActionDecider(ctx context.Context, workspaceID, token string, approved bool, decider WaitpointDecider) error {
	var stepID string
	err := s.db.QueryRowContext(ctx, `SELECT step_id FROM pipeline_waitpoints WHERE workspace_id = ? AND token = ?`, workspaceID, token).Scan(&stepID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // the decide path below answers an unknown token
	}
	if err != nil {
		return fmt.Errorf("waitpoints: check decider: %w", err)
	}
	if !IsCrewshipApprovalStepKey(stepID) {
		return nil
	}
	if decider.Kind == DeciderUser && decider.ID != "" {
		ok, err := HeldActionDeciderAllowed(ctx, s.db, workspaceID, decider.ID)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	s.recordRefusal(ctx, workspaceID, token, approved, decider, refusalReasonHeldAction)
	return fmt.Errorf("%w: a held routine action needs a workspace OWNER or ADMIN", ErrDeciderNotAllowed)
}

// HeldActionDeciderAllowed reports whether userID currently holds a role in
// HeldActionDeciderRoles in the workspace. The dispatcher asks it again at
// execution, so a decision by someone since demoted does not run.
func HeldActionDeciderAllowed(ctx context.Context, db *sql.DB, workspaceID, userID string) (bool, error) {
	var role string
	err := db.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("waitpoints: decider role: %w", err)
	}
	return HeldActionDeciderRoles[strings.ToUpper(role)], nil
}

// SetAuditJournal wires the configured journal writer so a refused decision
// appends the workspace chain with the configured key. Unwired stores use the
// default-key transactional append.
func (s *SQLWaitpointStore) SetAuditJournal(j journal.TransactionEmitter) {
	s.auditJournal = j
}

// recordRefusal writes the refused attempt to audit_logs (Settings Audit
// reads it) and to the chained journal, in one transaction: neither lands
// without the other. Best effort as a whole: a failed audit is logged and
// does not turn a refusal into anything else. The legacy row has no user_id
// (the actor is not a user) and names the actor in metadata; the waitpoint's
// own status is read back so the record states what the attempt left
// untouched. The journal entry references the run and step, never the
// callback token.
func (s *SQLWaitpointStore) recordRefusal(ctx context.Context, workspaceID, token string, approved bool, decider WaitpointDecider, reason string) {
	status, runID, stepID := "", "", ""
	_ = s.db.QueryRowContext(ctx,
		`SELECT status, pipeline_run_id, step_id FROM pipeline_waitpoints WHERE token = ? AND workspace_id = ?`,
		token, workspaceID).Scan(&status, &runID, &stepID)
	if workspaceID == "" {
		// The public callback resolves the token's own workspace before it
		// reaches the door; a refusal with no workspace cannot satisfy the
		// audit_logs FK, so log it and stop.
		slog.Default().Warn("waitpoint decision refused (no workspace to record it under)",
			"token", token, "actor_kind", decider.Kind, "actor_id", decider.ID)
		return
	}
	facts := map[string]any{
		"actor_kind":       string(decider.Kind),
		"actor_id":         decider.ID,
		"attempted":        map[bool]string{true: "approve", false: "deny"}[approved],
		"waitpoint_status": status,
		"pipeline_run_id":  runID,
		"reason":           reason,
	}
	meta, _ := json.Marshal(facts)
	if err := s.appendRefusalAudit(ctx, workspaceID, token, runID, stepID, decider, string(meta), facts); err != nil {
		slog.Default().Warn("waitpoint decision refusal audit write failed", "error", err, "token", token)
	}
}

func (s *SQLWaitpointStore) appendRefusalAudit(ctx context.Context, workspaceID, token, runID, stepID string, decider WaitpointDecider, meta string, facts map[string]any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_logs (id, workspace_id, user_id, action, entity_type, entity_id, metadata, created_at)
		VALUES (lower(hex(randomblob(16))), ?, NULL, ?, 'waitpoint', ?, ?, ?)`,
		workspaceID, AuditActionWaitpointDecisionRefused, token, meta, now); err != nil {
		return err
	}
	actor := journal.ActorSystem
	switch decider.Kind {
	case DeciderAgent:
		actor = journal.ActorAgent
	case DeciderUser:
		actor = journal.ActorUser
	}
	entry := journal.Entry{
		WorkspaceID: workspaceID, Type: journal.EntryAuditEntityUpdated, Severity: journal.SeverityWarn,
		ActorType: actor, ActorID: decider.ID, Summary: "Waitpoint decision refused",
		Payload: map[string]any{"action": AuditActionWaitpointDecisionRefused, "entity_type": "waitpoint", "metadata": facts},
		Refs:    map[string]any{"pipeline_run_id": runID, "step_id": stepID},
	}
	var notify func()
	if s.auditJournal != nil {
		notify, err = s.auditJournal.AppendTx(ctx, tx, entry)
	} else {
		notify, err = journal.AppendTx(ctx, tx, entry)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if notify != nil {
		notify()
	}
	return nil
}
