package restrictedpreflight

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

type Service struct {
	DB       *sql.DB
	Workflow *restrictedworkflow.Service
}

func authority(m restrictedworkflow.PreparedMetadata, routine string) string {
	b, _ := json.Marshal([]any{m.WorkspaceID, m.PrincipalID, m.MemberID, m.MemberRevision, m.AgentID, m.ExecutionProfile, routine})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ClaimAssignedIssue is an explicit opt-in producer. The host selects task
// inputs from the current issue; public callers cannot select an authority or
// provide a replacement brief. Preparing performs no model invocation.
func (s *Service) ClaimAssignedIssue(ctx context.Context, user, workspace, agent, issue, routine string) (restrictedworkflow.Receipt, error) {
	if s == nil || s.DB == nil || s.Workflow == nil {
		return restrictedworkflow.Receipt{}, ErrNoWork
	}
	initial, err := readSource(ctx, s.DB, user, workspace, agent, issue)
	if err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	// A matching durable receipt needs no new chat or prepared origin. The
	// private reader rechecks current source, role and exact origin authority.
	if receipt, found, err := s.duplicate(ctx, initial, user, routine); found || err != nil {
		return receipt, err
	}
	// Reject legacy ownership and exhausted budget before creating an origin.
	// Private capacity is checked after the dedup lookup inside the writer.
	pre, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	err = cheap(ctx, pre, initial, false)
	_ = pre.Rollback()
	if err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	input, _ := json.Marshal(map[string]string{"issue_id": initial.Issue, "title": initial.Title, "brief": initial.Description})
	if len(input) > 16384 {
		return restrictedworkflow.Receipt{}, ErrNoWork
	}
	p, err := s.Workflow.PrepareManualWithRights(ctx, user, workspace, routine, map[string]any{"task": string(input)}, "",
		[]access.Right{{Kind: "project", ID: initial.Project, Operation: "read"}}, "issue")
	if err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = s.Workflow.CancelPrepared(ctx, p)
		}
	}()
	m := p.Metadata()
	if m.AgentID != agent || m.PrincipalID != user || m.WorkspaceID != workspace {
		return restrictedworkflow.Receipt{}, ErrNoWork
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	defer tx.Rollback()
	// A SQLite writer lock precedes every source, authority and busy check.
	if _, err = tx.ExecContext(ctx, `UPDATE access_attempts SET completed_at=completed_at WHERE id=?`, m.OriginAttemptID); err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	current, err := readSource(ctx, tx, user, workspace, agent, issue)
	if err != nil || current.digest() != initial.digest() {
		return restrictedworkflow.Receipt{}, ErrNoWork
	}
	var old, state string
	err = tx.QueryRowContext(ctx, `SELECT r.workflow_id,j.state FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id
 WHERE r.issue_id=? AND r.principal_id=? AND r.source_hash=? AND r.authority_hash=? AND r.recipe_hash=?`, issue, user, current.digest(), authority(m, routine), m.RecipeHash).Scan(&old, &state)
	if err == nil {
		if state == "failed" || state == "canceled" {
			return restrictedworkflow.Receipt{}, ErrReconciliation
		}
		// Commit releases the writer before the receipt reader uses the pool.
		if err = tx.Commit(); err != nil {
			return restrictedworkflow.Receipt{}, err
		}
		receipt, e := s.Workflow.ReceiptForActor(ctx, user, workspace, old)
		if e == nil && receipt.State == "needs_reconciliation" {
			return restrictedworkflow.Receipt{}, ErrReconciliation
		}
		return receipt, e
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return restrictedworkflow.Receipt{}, err
	}
	// Failed/unknown effects remain held even after editing the source. A
	// separate explicit reconciliation path is required to authorize a retry.
	var ambiguous bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id WHERE r.issue_id=? AND (j.state IN ('failed','canceled') OR EXISTS(SELECT 1 FROM work_items w WHERE w.id=j.id AND w.state='needs_reconciliation')))`, issue).Scan(&ambiguous); err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	if ambiguous {
		return restrictedworkflow.Receipt{}, ErrReconciliation
	}
	if err = cheap(ctx, tx, current, true); err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO restricted_preflight_reservations(origin_attempt_id,workflow_id,workspace_id,principal_id,member_id,member_revision,agent_id,issue_id,project_id,source_hash,authority_hash,recipe_hash,work_revision,brief_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, m.OriginAttemptID, m.WorkflowID, workspace, user, m.MemberID, m.MemberRevision, agent, issue, current.Project, current.digest(), authority(m, routine), m.RecipeHash, current.Revision, current.BriefRevision)
	if err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	receipt, err := s.Workflow.EnqueuePrepared(ctx, tx, p)
	if err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return restrictedworkflow.Receipt{}, err
	}
	keep = true
	s.Workflow.Wake()
	return receipt, nil
}

func (s *Service) duplicate(ctx context.Context, src source, user, routine string) (restrictedworkflow.Receipt, bool, error) {
	m := restrictedworkflow.PreparedMetadata{WorkspaceID: src.Workspace, PrincipalID: user, AgentID: src.Agent}
	var recipe string
	err := s.DB.QueryRowContext(ctx, `SELECT wm.id,wm.access_revision,a.restricted_execution_profile,p.definition_json
 FROM workspace_members wm JOIN agents a ON a.workspace_id=wm.workspace_id AND a.id=? AND a.deleted_at IS NULL
 JOIN pipelines p ON p.workspace_id=wm.workspace_id AND p.slug=? AND p.status='active' AND p.deleted_at IS NULL
 WHERE wm.workspace_id=? AND wm.user_id=? AND wm.access_mode='restricted'`, src.Agent, routine, src.Workspace, user).Scan(&m.MemberID, &m.MemberRevision, &m.ExecutionProfile, &recipe)
	if errors.Is(err, sql.ErrNoRows) {
		return restrictedworkflow.Receipt{}, false, nil
	}
	if err != nil {
		return restrictedworkflow.Receipt{}, false, err
	}
	h := sha256.Sum256([]byte(recipe))
	var id, state string
	err = s.DB.QueryRowContext(ctx, `SELECT r.workflow_id,j.state FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id WHERE r.issue_id=? AND r.principal_id=? AND r.source_hash=? AND r.authority_hash=? AND r.recipe_hash=?`, src.Issue, user, src.digest(), authority(m, routine), hex.EncodeToString(h[:])).Scan(&id, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return restrictedworkflow.Receipt{}, false, nil
	}
	if err != nil {
		return restrictedworkflow.Receipt{}, false, err
	}
	if state == "failed" || state == "canceled" {
		return restrictedworkflow.Receipt{}, true, ErrReconciliation
	}
	r, err := s.Workflow.ReceiptForActor(ctx, user, src.Workspace, id)
	if err == nil && r.State == "needs_reconciliation" {
		return restrictedworkflow.Receipt{}, true, ErrReconciliation
	}
	return r, true, err
}

func cheap(ctx context.Context, tx *sql.Tx, s source, requireFree bool) error {
	if err := legacyBusy(ctx, tx, s.Issue); err != nil {
		return err
	}
	if requireFree {
		var busy bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM restricted_preflight_reservations r JOIN restricted_workflow_jobs j ON j.id=r.workflow_id WHERE r.issue_id=? AND j.state IN ('pending','running'))
 OR EXISTS(SELECT 1 FROM restricted_workflow_jobs WHERE workspace_id=? AND agent_id=? AND state IN ('pending','running'))
 OR EXISTS(SELECT 1 FROM work_items WHERE workspace_id=? AND agent_id=? AND state IN ('starting','running','needs_reconciliation'))
 OR EXISTS(SELECT 1 FROM assignments WHERE workspace_id=? AND assigned_to_id=? AND status NOT IN ('COMPLETED','FAILED','CANCELLED','TIMEOUT'))`, s.Issue, s.Workspace, s.Agent, s.Workspace, s.Agent, s.Workspace, s.Agent).Scan(&busy)
		if err != nil {
			return err
		}
		if busy {
			return ErrBusy
		}
	}
	statuses, err := paymaster.CheckTx(ctx, tx, paymaster.Scope{WorkspaceID: s.Workspace, CrewID: s.Crew, AgentID: s.Agent, MissionID: s.Issue})
	if err != nil {
		return err
	}
	hard := false
	for _, v := range statuses {
		if v.Budget.ScopeKind == paymaster.ScopeWorkspace && v.Budget.Mode == paymaster.ModeHard && v.Budget.LimitUSD > 0 {
			hard = true
		}
		if v.State == paymaster.StateExceeded {
			return fmt.Errorf("preflight budget exhausted")
		}
	}
	if !hard {
		return fmt.Errorf("preflight requires a workspace hard budget")
	}
	return nil
}

var _ func(context.Context, pipeline.PageActionQuery, string) error = CheckSource
