package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

// ScheduledAuthorizer rechecks the authority for an accepted cron occurrence
// immediately before execution. It does not execute work or enable a scheduler
// dispatcher by itself. Editing the prompt or cadence does not rewrite an
// already accepted item: its input remains the immutable acceptance snapshot.
// Disabling the schedule, deleting or moving its agent/crew/workspace, or
// staging the agent for review does change whether it may run.
type ScheduledAuthorizer struct {
	db *sql.DB
}

func NewScheduledAuthorizer(db *sql.DB) *ScheduledAuthorizer {
	return &ScheduledAuthorizer{db: db}
}

func (a *ScheduledAuthorizer) Authorize(ctx context.Context, assignment dispatch.Assignment) (dispatch.Decision, error) {
	item := assignment.Item
	if item == nil || item.Source != work.SourceSchedule || item.DomainKind != work.DomainAgentRun ||
		item.Class != work.ClassBackground || item.AuthorizedScope != "agent-schedule" {
		return dispatch.Refuse("work is not a scheduled agent run"), nil
	}
	var input ScheduledInput
	if err := json.Unmarshal([]byte(item.InputJSON), &input); err != nil ||
		input.Version != 1 || input.AgentID == "" || input.AgentID != item.AgentID ||
		input.Occurrence == "" || input.Occurrence != item.SourceRef {
		return dispatch.Refuse("scheduled work has invalid immutable input"), nil
	}
	sum := sha256.Sum256([]byte(item.InputJSON))
	if hex.EncodeToString(sum[:]) != item.InputSHA256 {
		return dispatch.Refuse("scheduled work input checksum changed"), nil
	}
	if _, err := time.Parse(time.RFC3339Nano, input.Occurrence); err != nil {
		return dispatch.Refuse("scheduled work has invalid occurrence identity"), nil
	}
	if a == nil || a.db == nil {
		return dispatch.Decision{}, errors.New("scheduler: dispatch authorizer has no database")
	}

	var workspaceID, status string
	var crewID, agentDeleted, workspaceDeleted sql.NullString
	var foundCrewID, crewWorkspace, crewDeleted sql.NullString
	var enabled int
	var cron sql.NullString
	err := a.db.QueryRowContext(ctx, `SELECT a.workspace_id, a.status, a.crew_id,
		a.deleted_at, a.schedule_enabled, a.schedule_cron, w.deleted_at,
		c.id, c.workspace_id, c.deleted_at
		FROM agents a JOIN workspaces w ON w.id = a.workspace_id
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ?`, item.AgentID).
		Scan(&workspaceID, &status, &crewID, &agentDeleted, &enabled, &cron, &workspaceDeleted,
			&foundCrewID, &crewWorkspace, &crewDeleted)
	if errors.Is(err, sql.ErrNoRows) {
		return dispatch.Refuse("scheduled agent or workspace no longer exists"), nil
	}
	if err != nil {
		return dispatch.Decision{}, fmt.Errorf("scheduler: read agent at dispatch: %w", err)
	}
	if agentDeleted.Valid || workspaceDeleted.Valid || workspaceID != item.WorkspaceID {
		return dispatch.Refuse("scheduled agent or workspace changed after acceptance"), nil
	}
	if enabled != 1 || !cron.Valid || cron.String == "" {
		return dispatch.Refuse("schedule was disabled while work waited"), nil
	}
	currentCrew := ""
	if crewID.Valid {
		currentCrew = crewID.String
	}
	if currentCrew != item.CrewID {
		return dispatch.Refuse("scheduled agent changed crew after acceptance"), nil
	}
	if currentCrew != "" {
		if !foundCrewID.Valid {
			return dispatch.Refuse("scheduled agent crew no longer exists"), nil
		}
		if crewDeleted.Valid || !crewWorkspace.Valid || crewWorkspace.String != item.WorkspaceID {
			return dispatch.Refuse("scheduled agent crew changed after acceptance"), nil
		}
	}
	if status == chatbridge.AgentStatusPendingReview {
		return dispatch.NotYet("scheduled agent awaits approval", 30*time.Second), nil
	}
	return dispatch.Allow(), nil
}

var _ dispatch.Authorizer = (*ScheduledAuthorizer)(nil)
