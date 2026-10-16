package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func (r *Router) waitpointDecisionAdapter() decisionSourceAdapter {
	return decisionSourceAdapter{SourceType: "waitpoint", Read: r.waitpointDecision, Upper: r.waitpointDecisionUpper, Scan: r.waitpointDecisionScan, Resolve: r.resolveWaitpointDecision}
}

// Candidates are private. The common paginator counts only successful Read
// projections, including forms and current audience, before emitting any hint.
const waitpointDecisionCandidates = `w.workspace_id=? AND w.kind='approval' AND COALESCE(p.invocation_source_run_id,'')<>'' AND p.invoking_agent_id=? AND EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=w.workspace_id AND j.trace_id=p.invocation_source_run_id AND j.agent_id=p.invoking_agent_id AND j.entry_type='run.started' AND json_valid(j.payload) AND json_extract(j.payload,'$.chat_id')=?)`

func (r *Router) waitpointDecisionUpper(ctx context.Context, a chataudience.Receipt) (int64, error) {
	var upper int64
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(w.rowid),0) FROM pipeline_waitpoints w JOIN pipeline_runs p ON p.id=w.pipeline_run_id AND p.workspace_id=w.workspace_id WHERE `+waitpointDecisionCandidates, a.WorkspaceID, a.AgentID, a.ChatID).Scan(&upper)
	return upper, err
}

func (r *Router) waitpointDecisionScan(ctx context.Context, a chataudience.Receipt, after, upper int64, limit int) ([]decisionSourceRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT w.chat_source_id,w.rowid FROM pipeline_waitpoints w JOIN pipeline_runs p ON p.id=w.pipeline_run_id AND p.workspace_id=w.workspace_id WHERE `+waitpointDecisionCandidates+` AND w.rowid>? AND w.rowid<=? ORDER BY w.rowid LIMIT ?`, a.WorkspaceID, a.AgentID, a.ChatID, after, upper, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selected := []decisionSourceRow{}
	for rows.Next() {
		var row decisionSourceRow
		if err = rows.Scan(&row.ID, &row.Position); err != nil {
			return nil, err
		}
		selected = append(selected, row)
	}
	return selected, rows.Err()
}

func waitpointAnswerPayload(source waitpointDecisionSource, answer chatentities.DecisionAnswer) (bool, string, error) {
	approved := answer.Action == "approve"
	if source.Form == "" {
		if answer.ActionID != "" || len(answer.Values) != 0 || answer.Action != "approve" && answer.Action != "reject" {
			return false, "", errDecisionInput
		}
		return approved, "", nil
	}
	if answer.Action != "answer" {
		return false, "", errDecisionInput
	}
	var form pipeline.DecisionForm
	if json.Unmarshal([]byte(source.Form), &form) != nil {
		return false, "", errDecisionInput
	}
	found := false
	for _, action := range form.Actions {
		if action.ID == answer.ActionID {
			approved, found = action.Approved, true
			break
		}
	}
	if !found {
		return false, "", errDecisionInput
	}
	data := map[string]any{}
	for key, raw := range answer.Values {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return false, "", errDecisionInput
		}
		if value == nil {
			return false, "", errDecisionInput
		}
		if text, ok := value.(string); ok && (!cardText(text, 0, 10000) || inbox.RedactSecrets(text) != text) {
			return false, "", errDecisionInput
		}
		data[key] = value
	}
	raw, err := json.Marshal(pipeline.DecisionAnswer{ActionID: answer.ActionID, Data: data})
	if err != nil {
		return false, "", errDecisionInput
	}
	payload, err := pipeline.NormalizeDecisionAnswer(&form, approved, string(raw))
	if err != nil {
		return false, "", fmt.Errorf("%w: %v", errDecisionInput, err)
	}
	return approved, payload, nil
}

func (r *Router) resolveWaitpointDecision(req *http.Request, op *decisionOperation, answer chatentities.DecisionAnswer) error {
	if op == nil || WorkspaceIDFromContext(req.Context()) != op.Audience.WorkspaceID || op.Key == "" || op.Hash == "" || answer.SourceID == "" || answer.ExpectedVersion != op.Version || UserFromContext(req.Context()) == nil || UserFromContext(req.Context()).ID != op.Audience.UserID {
		return errDecisionDenied
	}
	if r.PipelinesHandler == nil {
		return errors.New("waitpoint store not wired")
	}
	store, ok := r.PipelinesHandler.waitpoints.(*pipeline.SQLWaitpointStore)
	if !ok {
		return errors.New("waitpoint store does not support source transactions")
	}
	ctx := req.Context()
	tx, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if _, err = tx.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { _, _ = tx.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") }()
	live, err := chataudience.CaptureTrustedQuery(ctx, tx, op.Audience.ChatID, op.Audience.UserID, &op.Audience)
	if err != nil {
		return err
	}
	if live == nil {
		return errDecisionDenied
	}
	source, err := readWaitpointDecision(ctx, tx, live.WorkspaceID, answer.SourceID)
	if err != nil {
		return err
	}
	if strings.Contains(op.Key, source.Token) {
		return errDecisionInput
	}
	intent, err := json.Marshal(answer)
	if err != nil || strings.Contains(string(intent), source.Token) {
		return errDecisionInput
	}
	// Recheck projection eligibility and provenance under the reserved writer.
	if _, err = waitpointDecisionProjection(ctx, tx, *live, source); err != nil {
		return err
	}
	if source.OperationKey == op.Key {
		if source.OperationHash != op.Hash || source.Actor != live.UserID {
			return errDecisionKeyCollision
		}
		return nil // Original effect; current authorized source is returned by dispatcher.
	}
	var role string
	if err = tx.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE id=? AND workspace_id=? AND user_id=? AND access_mode='trusted'`, live.MemberID, live.WorkspaceID, live.UserID).Scan(&role); err != nil {
		return err
	}
	if !canRole(role, "update") || !canScope(ctx, "workspace:admin") {
		return errDecisionDenied
	}
	if source.version() != op.Version {
		return errDecisionVersion
	}
	approved, payload, err := waitpointAnswerPayload(source, answer)
	if err != nil {
		return err
	}
	if strings.Contains(payload, source.Token) {
		return errDecisionInput
	}
	result, decisionErr := store.DecideTx(ctx, tx, live.WorkspaceID, source.Token, approved, pipeline.WaitpointDecider{Kind: pipeline.DeciderUser, ID: live.UserID}, payload)
	if decisionErr != nil && (result == nil || !result.Applied) {
		if errors.Is(decisionErr, pipeline.ErrAlreadyDecided) {
			return errDecisionVersion
		}
		return decisionErr
	}
	if result == nil || !result.Applied {
		return errDecisionVersion
	}
	if !result.Expired {
		if _, err = tx.ExecContext(ctx, `UPDATE pipeline_waitpoints SET answer_operation_key=?,answer_operation_hash=? WHERE workspace_id=? AND token=?`, op.Key, op.Hash, live.WorkspaceID, source.Token); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	// Postcommit effects use the shared pool; release the reserved connection
	// first, including when all configured connections were confirming at once.
	_ = tx.Close()
	store.AfterDecide(context.WithoutCancel(ctx), result)
	if result.Expired {
		return errDecisionVersion
	}
	// Match canonical Inbox completion for async parked workflows. Legacy
	// blocking listeners were notified by AfterDecide; resume is then a no-op.
	r.PipelinesHandler.newExecutor().ResumeAfterApproval(result.PipelineRunID, r.logger)
	return nil
}
