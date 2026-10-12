package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

type assignmentWorkAuthorizer struct{ h *AssignmentHandler }

func (auth assignmentWorkAuthorizer) Authorize(ctx context.Context, a dispatch.Assignment) (dispatch.Decision, error) {
	if a.Item == nil || a.Item.Source != work.SourceAssignment || a.Item.DomainKind != work.DomainAssignment {
		return dispatch.Refuse("unsupported assignment work kind"), nil
	}
	var in assignmentWorkInput
	if json.Unmarshal([]byte(a.Item.InputJSON), &in) != nil || in.Version != 1 || in.AssignmentID != a.Item.DomainID || in.AgentID != a.Item.AgentID || in.WorkspaceID != a.Item.WorkspaceID || in.CrewID != a.Item.CrewID {
		return dispatch.Refuse("assignment input identity mismatch"), nil
	}
	var ws, agent, crew, task, status, agentStatus, creator, actor, actorCrew, session string
	var cancelled sql.NullString
	err := auth.h.db.QueryRowContext(ctx, `SELECT asn.workspace_id,asn.assigned_to_id,COALESCE(ag.crew_id,''),asn.task,asn.status,ag.status,COALESCE(asn.created_by_user_id,''),COALESCE(asn.assigned_by_id,''),COALESCE(actor.crew_id,''),COALESCE(asn.session_id,''),asn.cancel_requested_at
 FROM assignments asn JOIN agents ag ON ag.id=asn.assigned_to_id AND ag.workspace_id=asn.workspace_id
 JOIN crews cr ON cr.id=ag.crew_id AND cr.workspace_id=ag.workspace_id JOIN workspaces w ON w.id=ag.workspace_id
 LEFT JOIN agents actor ON actor.id=asn.assigned_by_id AND actor.workspace_id=asn.workspace_id AND actor.deleted_at IS NULL
 WHERE asn.id=? AND ag.deleted_at IS NULL AND cr.deleted_at IS NULL AND w.deleted_at IS NULL`, in.AssignmentID).Scan(&ws, &agent, &crew, &task, &status, &agentStatus, &creator, &actor, &actorCrew, &session, &cancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return dispatch.Refuse("assignment target or workspace no longer exists"), nil
	}
	if err != nil {
		return dispatch.Decision{}, fmt.Errorf("authorize assignment: %w", err)
	}
	if ws != in.WorkspaceID || agent != in.AgentID || crew != in.CrewID || task != in.Task || session != a.Item.SessionID || creator != a.Item.AuthorizedByUserID || actor != in.AuthorAgentID || a.Item.AuthorizedScope != "assignment:"+in.AssignmentID {
		return dispatch.Refuse("assignment scope or immutable input changed"), nil
	}
	if status == "COMPLETED" || status == "FAILED" || status == "CANCELLED" {
		return dispatch.Refuse("assignment domain already terminal"), nil
	}
	if cancelled.Valid {
		_, err = work.NewStore(auth.h.db).RequestCancel(ctx, a.Item.ID, "assignment", "assignment cancellation requested")
		return dispatch.Allow(), err
	}
	if agentStatus == chatbridge.AgentStatusPendingReview {
		return dispatch.NotYet("assignment target is awaiting approval", 30*time.Second), nil
	}
	if creator != "" {
		var count int
		if err = auth.h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, creator).Scan(&count); err != nil {
			return dispatch.Decision{}, err
		}
		if count == 0 {
			return dispatch.Refuse("assignment author no longer belongs to workspace"), nil
		}
	}
	if actor != "" && actorCrew == "" {
		return dispatch.Refuse("assignment author agent is unavailable"), nil
	}
	if actorCrew != "" && actorCrew != crew {
		connected, err := AreCrewsConnected(ctx, auth.h.db, actorCrew, crew)
		if err != nil {
			return dispatch.Decision{}, err
		}
		if !connected {
			return dispatch.Refuse("assignment crews are no longer connected"), nil
		}
	}
	allowed, err := auth.h.authorizeConversationAssignment(ctx, in.AssignmentID)
	if err != nil {
		return dispatch.Decision{}, err
	}
	if !allowed {
		return dispatch.Refuse("conversation assignment permission was revoked"), nil
	}
	if !assignmentOwnsExecutionLock(ctx, a) && auth.h.agentRunLock != nil && auth.h.agentRunLock.InFlight(agent) {
		return dispatch.NotYet("agent has another live execution", time.Second), nil
	}
	return dispatch.Allow(), nil
}

func assignmentOwnsExecutionLock(ctx context.Context, a dispatch.Assignment) bool {
	e := ownedAssignmentExecution(ctx)
	return e != nil && e.assignment.Item != nil && a.Item != nil && e.assignment.Item.ID == a.Item.ID && e.assignment.RunID == a.RunID && e.assignment.Generation == a.Generation
}
