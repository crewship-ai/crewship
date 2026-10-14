package api

import (
	"context"
	"database/sql"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Share the durable projection worker across public and internal mutation doors.
// The host boot worker drains the same table; this value has no daemon lifecycle.
// Projection-only users have no dispatcher: their drains select journal jobs
// only, leaving mention attempts untouched for the fully wired host worker.
func (e issueEvents) mutationDelivery() *IssueHandler {
	return &IssueHandler{db: e.db, hub: e.hub, logger: e.logger, journal: e.journal}
}
func (h *InternalIssueHandler) mutationDelivery() *IssueHandler {
	d := h.events().mutationDelivery()
	d.mentionDispatch = h.mentionDispatch
	return d
}

// Resolve presentation data using the mutation transaction, never a second DB
// connection while its write lock is held. Authorization is the caller's job.
func persistIssueCommentMentionsTx(ctx context.Context, tx *sql.Tx, workspace, mission, identifier, comment, body, actorType, actorID, sourceRunID string) error {
	var title, crew, name string
	err := tx.QueryRowContext(ctx, `SELECT m.title,COALESCE(m.crew_id,''),COALESCE(CASE WHEN ?='agent' THEN (SELECT name FROM agents WHERE id=? AND workspace_id=m.workspace_id) ELSE (SELECT full_name FROM users WHERE id=?) END,?) FROM missions m WHERE m.id=? AND m.workspace_id=?`, actorType, actorID, actorID, actorID, mission, workspace).Scan(&title, &crew, &name)
	if err != nil {
		return err
	}
	autonomousPipeline := sourceRunID != "" && hostPipelineRunID(ctx) == sourceRunID && runauthority.OriginIDFromContext(ctx) == ""
	var sourceCrew string
	if autonomousPipeline {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(crew_id,'') FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, actorID, workspace).Scan(&sourceCrew); err != nil {
			return err
		}
	}
	return persistCommentMentionsTx(ctx, tx, mentionContext{WorkspaceID: workspace, MissionID: mission, Identifier: identifier, IssueTitle: title, IssueCrewID: crew, CommentID: comment, CommentBody: body, AuthorType: actorType, AuthorID: actorID, AuthorName: name, SourceRunID: sourceRunID, AutonomousPipeline: autonomousPipeline, AutonomousPipelineCrewID: sourceCrew})
}
