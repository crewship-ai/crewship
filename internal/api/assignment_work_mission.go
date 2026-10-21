package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/missionactivity"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Root planning and delegated /assign work can belong to a mission without a
// mission_task. Their completion comment and activity commit with the domain
// result, using the assignment identity so outbox replay cannot duplicate them.
func assignmentMissionCommentTx(ctx context.Context, tx *sql.Tx, id, acceptedAgent, status, outcome, result, message, stamp string) error {
	var mission, workspace, agent, name string
	err := tx.QueryRowContext(ctx, `SELECT m.id,m.workspace_id,ag.id,ag.name FROM assignments a JOIN missions m ON m.id=COALESCE(NULLIF(a.mission_id,''),NULLIF(a.group_id,''),a.chat_id) AND m.workspace_id=a.workspace_id JOIN agents ag ON ag.id=? AND ag.workspace_id=a.workspace_id WHERE a.id=? AND NOT EXISTS(SELECT 1 FROM mission_tasks t WHERE t.assignment_id=a.id)`, acceptedAgent, id).Scan(&mission, &workspace, &agent, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	commentID := "assignment-comment:" + id
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_comments WHERE id=?`, commentID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	summary := orchestrator.ParseHandoff(result).Summary
	if summary == "" {
		summary = result
	}
	if len(summary) > 4096 {
		summary = summary[:4096]
	}
	action := "task_completed"
	body := fmt.Sprintf("**%s completed their work.**\n\n%s", name, summary)
	switch {
	case status == "CANCELLED":
		action = "task_cancelled"
		body = fmt.Sprintf("**%s's work was cancelled.**", name)
	case status == "FAILED":
		action = "task_failed"
		body = fmt.Sprintf("**%s encountered an issue.** %s", name, userFacingAssignmentError(message))
	case outcome == orchestrator.OutcomeNeedsHuman:
		action = "commented"
		body = fmt.Sprintf("**%s needs your input.**\n\n%s", name, summary)
	case outcome == orchestrator.OutcomeFailed || outcome == orchestrator.OutcomePartial:
		action = "task_failed"
		body = fmt.Sprintf("**%s reported an incomplete result.**\n\n%s", name, summary)
	}
	written, err := orchestrator.WriteAssignmentResultCommentTx(ctx, tx, id, workspace, mission, agent, commentID, body, stamp)
	if err != nil {
		if errors.Is(err, runauthority.ErrUnavailable) || errors.Is(err, runauthority.ErrIdentityMismatch) || errors.Is(err, runauthority.ErrRequesterStale) {
			// Revoked requester authority withholds publication; terminal work
			// still settles and releases its slot without exposing result text.
			return nil
		}
		return err
	}
	if !written {
		return nil
	}
	_, err = missionactivity.EmitTx(ctx, tx, missionactivity.Entry{ID: "assignment-activity:" + id, MissionID: mission, ActorType: "agent", ActorID: agent, Action: action, Details: body, SourceKind: "assignment", SourceID: id})
	return err
}
