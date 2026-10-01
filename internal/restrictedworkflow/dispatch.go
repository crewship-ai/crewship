package restrictedworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func (s *Service) checkJob(ctx context.Context, q pipeline.PageActionQuery, j job, handle string) error {
	expires, err := time.Parse(time.RFC3339Nano, j.Expires)
	if err != nil || ((j.State == "pending" || j.State == "running") && !time.Now().Before(expires)) {
		return ErrDenied
	}
	var one int
	err = q.QueryRowContext(ctx, `SELECT 1 FROM access_attempts a
 JOIN workspace_members wm ON wm.id=a.member_id AND wm.workspace_id=a.workspace_id AND wm.user_id=a.principal_id AND wm.access_revision=a.member_revision AND wm.access_mode='restricted'
 JOIN workspaces w ON w.id=a.workspace_id AND w.deleted_at IS NULL
 JOIN chats c ON c.id=a.chat_id AND c.workspace_id=a.workspace_id AND c.agent_id=a.agent_id AND c.created_by=a.principal_id AND c.visibility='private' AND c.authority_generation=a.chat_generation AND c.authority_revision=a.chat_revision
 JOIN agents ag ON ag.id=a.agent_id AND ag.workspace_id=a.workspace_id AND ag.deleted_at IS NULL AND ag.restricted_execution_profile=?
 JOIN access_grants g ON g.member_id=wm.id AND g.resource_kind='agent' AND g.agent_id=ag.id AND g.operation='run'
 JOIN pipelines p ON p.id=? AND p.workspace_id=a.workspace_id AND p.deleted_at IS NULL AND p.status='active' AND p.definition_json=?
 WHERE a.id=? AND a.handle_hash=? AND a.workspace_id=? AND a.principal_id=? AND a.member_id=? AND a.member_revision=? AND a.agent_id=? AND a.chat_id=? AND a.revoked_at IS NULL AND a.completed_at IS NOT NULL AND a.parent_id IS NULL AND a.admission_operation='run' AND a.context_audience=''`, j.Profile, j.Pipeline, j.Recipe, j.Origin, hash(handle), j.Workspace, j.Principal, j.Member, j.Revision, j.Agent, j.Chat).Scan(&one)
	if err != nil || hash(j.Recipe) != j.RecipeHash {
		return ErrDenied
	}
	if j.SourceFacet != "" {
		if j.SourceFacet != "issue" || s.SourceChecker == nil || s.SourceChecker(ctx, q, j.Origin) != nil {
			return ErrDenied
		}
	}
	var rights []access.Right
	if json.Unmarshal([]byte(j.Rights), &rights) != nil {
		return ErrDenied
	}
	for _, right := range rights {
		if right.ID == "" {
			return ErrDenied
		}
		if right.Kind == "agent" && (right.Operation == "run" || right.Operation == "delegate") {
			var allowed int
			if q.QueryRowContext(ctx, `SELECT 1 FROM access_grants g JOIN agents a ON a.id=g.agent_id AND a.workspace_id=? AND a.deleted_at IS NULL WHERE g.member_id=? AND g.resource_kind='agent' AND g.agent_id=? AND g.operation=?`, j.Workspace, j.Member, right.ID, right.Operation).Scan(&allowed) != nil {
				return ErrDenied
			}
			continue
		}
		if right.Kind != "project" || right.Operation != "read" {
			return ErrDenied
		}
		var allowed int
		if q.QueryRowContext(ctx, `SELECT 1 FROM access_grants g JOIN projects p ON p.id=g.project_id AND p.workspace_id=? WHERE g.member_id=? AND g.resource_kind='project' AND g.project_id=? AND g.operation='read'`, j.Workspace, j.Member, right.ID).Scan(&allowed) != nil {
			return ErrDenied
		}
	}
	if err = s.checkGraph(ctx, q, j); err != nil {
		return err
	}
	if j.Source == "manual" {
		return manualPolicy(ctx, q, j.Principal, j.Workspace)
	}
	if j.Source != "page" || !j.Page.Valid {
		return ErrDenied
	}
	var action pipeline.PageActionInvocation
	if json.Unmarshal([]byte(j.PageAction), &action) != nil || action.PageID != j.Page.String || action.PipelineID != j.Pipeline {
		return ErrDenied
	}
	var spec string
	if err = q.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id=? AND workspace_id=?`, j.Page.String, j.Workspace).Scan(&spec); err != nil || hash(spec) != j.PageHash {
		return ErrDenied
	}
	if err = pipeline.CheckDeclaredPageAction(ctx, q, j.Principal, j.Workspace, action); err != nil {
		return ErrDenied
	}
	return nil
}
func (s *Service) load(ctx context.Context, id string) (job, error) {
	var j job
	err := s.db.QueryRowContext(ctx, `SELECT id,workspace_id,principal_id,member_id,member_revision,pipeline_id,recipe_hash,recipe_json,agent_id,execution_profile,chat_id,origin_attempt_id,origin_handle_ciphertext,source_kind,page_id,page_action_json,page_spec_hash,inputs_json,idempotency_key_hash,state,outputs_json,created_at,fire_at,expires_at,additional_rights_json,source_facet,graph_json,graph_hash,proofs_json FROM restricted_workflow_jobs WHERE id=?`, id).Scan(&j.ID, &j.Workspace, &j.Principal, &j.Member, &j.Revision, &j.Pipeline, &j.RecipeHash, &j.Recipe, &j.Agent, &j.Profile, &j.Chat, &j.Origin, &j.Handle, &j.Source, &j.Page, &j.PageAction, &j.PageHash, &j.Inputs, &j.Idempotency, &j.State, &j.Outputs, &j.Created, &j.FireAt, &j.Expires, &j.Rights, &j.SourceFacet, &j.Graph, &j.GraphHash, &j.Proofs)
	return j, err
}

// DispatchNext claims a durable queued invocation. It never accepts task JSON,
// execution handles, principals or runner configuration from the caller.
func (s *Service) DispatchNext(ctx context.Context) (bool, error) {
	s.dispatch.Lock()
	defer s.dispatch.Unlock()
	if quiesce.DefaultHolds().Held(quiesce.HoldQueue) {
		return false, nil
	}
	writer, admitted := quiesce.Enter(ctx)
	if !admitted {
		return false, nil
	}
	defer writer.Leave()
	ctx = writer.Context()
	var next string
	err := s.db.QueryRowContext(ctx, `UPDATE restricted_workflow_jobs SET state='running' WHERE id=(SELECT id FROM restricted_workflow_jobs WHERE state='pending' AND fire_at<=? ORDER BY fire_at,id LIMIT 1) AND state='pending' RETURNING id`, tsformat.Format(time.Now())).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	j, err := s.load(ctx, next)
	if err != nil {
		return true, err
	}
	completed := false
	defer func() {
		if !completed {
			s.fail(j)
		}
	}()
	handle, err := encryption.Decrypt(j.Handle)
	if err != nil {
		return true, ErrDenied
	}
	if j.Expires <= tsformat.Format(time.Now()) || s.checkJob(ctx, s.db, j, handle) != nil {
		return true, ErrDenied
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	monitorDone := make(chan struct{})
	defer close(monitorDone)
	go func(monitored job) {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-monitorDone:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if s.checkJob(runCtx, s.db, monitored, handle) != nil {
					cancel()
					return
				}
			}
		}
	}(j)
	outputs, proofs, err := s.executeGraph(runCtx, j, handle)
	if err != nil || s.checkJob(runCtx, s.db, j, handle) != nil {
		return true, ErrDenied
	}
	raw, err := json.Marshal(outputs)
	if err != nil || len(raw) > 256<<10 {
		return true, ErrDenied
	}
	j.Outputs, j.Proofs = string(raw), proofs
	if _, err = s.readGraphOutputs(runCtx, j); err != nil {
		return true, ErrDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE restricted_workflow_jobs SET state=state WHERE id=? AND state='running'`, j.ID); err != nil {
		return true, err
	}
	if err = s.checkJob(ctx, tx, j, handle); err != nil {
		return true, err
	}
	if _, err = s.readGraphOutputs(ctx, j); err != nil {
		return true, ErrDenied
	}
	updated, err := tx.ExecContext(ctx, `UPDATE restricted_workflow_jobs SET state='completed',outputs_json=?,proofs_json=?,finished_at=? WHERE id=? AND state='running'`, string(raw), proofs, tsformat.Format(time.Now()), j.ID)
	if err != nil {
		return true, err
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return true, ErrDenied
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	completed = true
	if (access.Store{DB: s.db}).RecordOutcome(ctx, handle, "completed") != nil {
		slog.Error("record restricted workflow outcome failed")
	}
	return true, nil
}
func (s *Service) fail(j job) {
	clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Only the origin loaded from the private durable job is revoked here.
	_, _ = s.db.ExecContext(clean, `UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, tsformat.Format(time.Now()), j.Origin)
	_, _ = s.db.ExecContext(clean, `UPDATE restricted_workflow_jobs SET state='failed',finished_at=? WHERE id=? AND state='running'`, tsformat.Format(time.Now()), j.ID)
	if handle, err := encryption.Decrypt(j.Handle); err == nil {
		if (access.Store{DB: s.db}).RecordOutcome(clean, handle, "failed") != nil {
			slog.Error("record restricted workflow outcome failed")
		}
	}
}
func (s *Service) Result(ctx context.Context, user, workspace, id string) (Result, error) {
	var result Result
	j, err := s.load(ctx, id)
	if err != nil || j.Principal != user || j.Workspace != workspace {
		return result, ErrDenied
	}
	handle, err := encryption.Decrypt(j.Handle)
	if err != nil || s.checkJob(ctx, s.db, j, handle) != nil {
		return result, ErrDenied
	}
	result = Result{ID: j.ID, State: j.State, CreatedAt: j.Created, Outputs: map[string]string{}}
	if j.State == "completed" {
		result.Outputs, err = s.readGraphOutputs(ctx, j)
		if err != nil {
			return Result{}, ErrDenied
		}
	}
	if err = (access.Store{DB: s.db}).CheckContextAttempt(ctx, handle); err != nil {
		return Result{}, ErrDenied
	}
	return result, nil
}

func (s *Service) ResultsForActor(ctx context.Context, user, workspace string) ([]Result, error) {
	m, err := (access.Store{DB: s.db}).Membership(ctx, user, workspace)
	if err != nil || m.Mode != "restricted" {
		return nil, ErrDenied
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM restricted_workflow_jobs WHERE principal_id=? AND workspace_id=? ORDER BY created_at DESC,id LIMIT 100`, user, workspace)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []Result{}
	for _, id := range ids {
		entry, err := s.Result(ctx, user, workspace, id)
		if err == nil {
			result = append(result, entry)
		} else if !errors.Is(err, ErrDenied) {
			return nil, err
		}
	}
	return result, nil
}
