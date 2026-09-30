// Package restrictedpreflight claims project-backed issue inputs for the private
// workflow queue. A cheap source read is not permission to execute a routine.
package restrictedpreflight

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

var ErrNoWork = errors.New("no authorized runnable issue")
var ErrBusy = errors.New("issue is busy")
var ErrReconciliation = errors.New("issue requires execution reconciliation")

// source contains only database-selected issue data. Its digest binds the brief,
// assignment, project and work revision; a caller cannot supply this authority.
type source struct {
	Issue, Workspace, Crew, Agent, Project, ProjectStatus           string
	Title, Description, Status, Owner, AssigneeType, Assignee, Lead string
	Mode, Worker                                                    string
	Revision, BriefRevision                                         int64
}

func (s source) digest() string {
	b, _ := json.Marshal(s)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// readSource checks exact current project.read and agent.run grants before any
// source content leaves the database. Projectless and paused/terminal projects
// do not produce work in this first adapter.
func readSource(ctx context.Context, q pipeline.PageActionQuery, user, workspace, agent, issue string) (source, error) {
	var s source
	err := q.QueryRowContext(ctx, `SELECT m.id,m.workspace_id,m.crew_id,m.delegate_agent_id,m.project_id,p.status,
 m.title,COALESCE(m.description,''),m.status,COALESCE(m.owner_user_id,''),COALESCE(m.assignee_type,''),
 COALESCE(m.assignee_id,''),COALESCE(m.lead_agent_id,''),iw.mode,COALESCE(iw.worker_user_id,''),iw.revision,iw.brief_revision
 FROM missions m JOIN issue_work iw ON iw.mission_id=m.id
 JOIN projects p ON p.id=m.project_id AND p.workspace_id=m.workspace_id
 JOIN agents a ON a.id=m.delegate_agent_id AND a.workspace_id=m.workspace_id AND a.crew_id=m.crew_id AND a.deleted_at IS NULL
 JOIN crews c ON c.id=m.crew_id AND c.workspace_id=m.workspace_id AND c.deleted_at IS NULL
 JOIN workspaces w ON w.id=m.workspace_id AND w.deleted_at IS NULL
 JOIN workspace_members wm ON wm.workspace_id=m.workspace_id AND wm.user_id=? AND wm.access_mode='restricted'
 WHERE m.id=? AND m.workspace_id=? AND m.delegate_agent_id=? AND m.mission_type='issue'
 AND length(m.title)+length(COALESCE(m.description,''))<=12000
 AND m.status='TODO' AND iw.mode='agent' AND p.status IN ('backlog','planned','in_progress')
 AND EXISTS(SELECT 1 FROM access_grants g WHERE g.member_id=wm.id AND g.resource_kind='project' AND g.project_id=p.id AND g.operation='read')
 AND EXISTS(SELECT 1 FROM access_grants g WHERE g.member_id=wm.id AND g.resource_kind='agent' AND g.agent_id=a.id AND g.operation='run')
 AND NOT EXISTS(SELECT 1 FROM mission_relations rel JOIN missions blocker
 ON blocker.id=CASE WHEN rel.relation_type='blocks' THEN rel.source_id ELSE rel.target_id END
 WHERE ((rel.relation_type='blocks' AND rel.target_id=m.id) OR (rel.relation_type='blocked_by' AND rel.source_id=m.id))
 AND blocker.status NOT IN ('DONE','CANCELLED'))`, user, issue, workspace, agent).Scan(
		&s.Issue, &s.Workspace, &s.Crew, &s.Agent, &s.Project, &s.ProjectStatus, &s.Title, &s.Description, &s.Status,
		&s.Owner, &s.AssigneeType, &s.Assignee, &s.Lead, &s.Mode, &s.Worker, &s.Revision, &s.BriefRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return source{}, ErrNoWork
	}
	if err != nil {
		return source{}, err
	}
	return s, nil
}

func legacyBusy(ctx context.Context, q pipeline.PageActionQuery, issue string) error {
	var busy bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM assignments WHERE
 (mission_id=? OR chat_id=? OR group_id=?) AND status NOT IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT'))
 OR EXISTS(SELECT 1 FROM issue_executions WHERE mission_id=? AND stage IN ('working','reviewing'))`, issue, issue, issue, issue).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return ErrBusy
	}
	return nil
}

// CheckSource is installed before starting the private queue. It rechecks the
// immutable origin's issue binding during enqueue, dispatch and output delivery.
func CheckSource(ctx context.Context, q pipeline.PageActionQuery, origin string) error {
	var user, workspace, agent, issue, digest string
	err := q.QueryRowContext(ctx, `SELECT principal_id,workspace_id,agent_id,issue_id,source_hash
 FROM restricted_preflight_reservations WHERE origin_attempt_id=?`, origin).Scan(&user, &workspace, &agent, &issue, &digest)
	if err != nil {
		return ErrNoWork
	}
	s, err := readSource(ctx, q, user, workspace, agent, issue)
	if err != nil {
		return err
	}
	if s.digest() != digest {
		return ErrNoWork
	}
	return nil
}
