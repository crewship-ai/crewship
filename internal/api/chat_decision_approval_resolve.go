package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func (r *Router) runApprovalDecisionAdapter() decisionSourceAdapter {
	return decisionSourceAdapter{SourceType: "run_approval", Read: r.runApprovalDecision, Upper: r.runApprovalUpper, Scan: r.runApprovalScan, Resolve: r.resolveRunApprovalDecision}
}
func (r *Router) resolveRunApprovalDecision(req *http.Request, op *decisionOperation, answer chatentities.DecisionAnswer) error {
	if op == nil || answer.SourceID == "" || answer.ExpectedVersion != op.Version || answer.ActionID != "" || answer.Values == nil || len(answer.Values) != 0 || (answer.Action != "approve" && answer.Action != "reject") {
		return errDecisionInput
	}
	ctx := req.Context()
	a := op.Audience
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") }()
	live, err := chataudience.CaptureTrustedQuery(ctx, conn, a.ChatID, a.UserID, &a)
	if err != nil {
		return err
	}
	if live == nil {
		return errDecisionDenied
	}
	s, err := readRunApprovalSource(ctx, conn, a, answer.SourceID)
	if err != nil {
		return err
	}
	var key, hash string
	if err = conn.QueryRowContext(ctx, `SELECT COALESCE(answer_operation_key,''),COALESCE(answer_operation_hash,'') FROM approvals_queue WHERE workspace_id=? AND id=?`, a.WorkspaceID, answer.SourceID).Scan(&key, &hash); err != nil {
		return err
	}
	if key == op.Key && key != "" {
		if hash != op.Hash {
			return errDecisionKeyCollision
		}
		return nil
	}
	var role string
	if err = conn.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE id=? AND workspace_id=? AND user_id=? AND access_mode='trusted'`, a.MemberID, a.WorkspaceID, a.UserID).Scan(&role); err != nil {
		return err
	}
	if (role != "OWNER" && role != "ADMIN") || !canScope(ctx, "workspace:admin") {
		return errDecisionDenied
	}
	if s.version() != op.Version || s.Row.Status != harbormaster.StatusPending || (s.Row.TimeoutAt != nil && !s.Row.TimeoutAt.After(time.Now())) {
		return errDecisionVersion
	}
	if answer.Action == "approve" {
		origin, err := runauthority.Resolve(ctx, conn, s.Run, a.AgentID, a.WorkspaceID)
		if err != nil || origin.TriggerType != "USER" || origin.HumanAuthority == nil || *origin.HumanAuthority != s.Requester || origin.ChatID != a.ChatID || origin.TriggerMessageID != s.Trigger {
			return errDecisionDenied
		}
	}
	status := harbormaster.StatusApproved
	if answer.Action == "reject" {
		status = harbormaster.StatusDenied
	}
	row, err := harbormaster.DecideTx(ctx, conn, a.WorkspaceID, answer.SourceID, status, a.UserID, "")
	if errors.Is(err, harbormaster.ErrDecisionDenied) {
		return errDecisionDenied
	}
	if errors.Is(err, harbormaster.ErrNotPending) {
		return errDecisionVersion
	}
	if errors.Is(err, harbormaster.ErrNotFound) {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `UPDATE approvals_queue SET answer_operation_key=?,answer_operation_hash=? WHERE workspace_id=? AND id=?`, op.Key, op.Hash, a.WorkspaceID, answer.SourceID); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	harbormaster.AfterDecide(context.WithoutCancel(ctx), r.db, r.Journal(), row, status, a.UserID, "")
	return nil
}
