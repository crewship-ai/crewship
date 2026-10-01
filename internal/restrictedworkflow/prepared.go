package restrictedworkflow

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// PreparedInvocation is a host-only authority capsule. It is not serializable
// and cannot be reconstructed from a public job or attempt identifier.
type PreparedInvocation struct {
	owner  *Service
	job    job
	handle string
	rights []access.Right
	facet  string
}
type RightsExecutor interface {
	ExecuteRunWithRights(context.Context, string, string, string, string, []access.Right, func(string, string) error) error
}
type PreparedMetadata struct {
	OriginAttemptID, WorkspaceID, PrincipalID, MemberID, AgentID, RecipeHash, ChatID, ExecutionProfile, WorkflowID, ExecutionHash string
	MemberRevision                                                                                                                int64
}

func (p *PreparedInvocation) Metadata() PreparedMetadata {
	if p == nil {
		return PreparedMetadata{}
	}
	j := p.job
	return PreparedMetadata{j.Origin, j.Workspace, j.Principal, j.Member, j.Agent, j.RecipeHash, j.Chat, j.Profile, j.ID, j.GraphHash, j.Revision}
}
func (s *Service) PrepareManualWithRights(ctx context.Context, user, workspace, slug string, inputs map[string]any, expectedHash string, rights []access.Right, sourceFacet string) (*PreparedInvocation, error) {
	if sourceFacet != "issue" || len(rights) != 1 || rights[0].Kind != "project" || rights[0].Operation != "read" || rights[0].ID == "" {
		return nil, ErrDenied
	}
	if _, ok := s.executor.(RightsExecutor); !ok {
		return nil, ErrDenied
	}
	if err := manualPolicy(ctx, s.db, user, workspace); err != nil {
		return nil, err
	}
	p := &PreparedInvocation{rights: append([]access.Right(nil), rights...), facet: sourceFacet}
	if _, err := s.admit(ctx, user, workspace, slug, inputs, expectedHash, 0, nil, "", p); err != nil {
		return nil, err
	}
	return p, nil
}
func (s *Service) CancelPrepared(ctx context.Context, p *PreparedInvocation) error {
	if p == nil || p.owner != s || p.handle == "" {
		return ErrDenied
	}
	clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return (access.Store{DB: s.db}).RevokeAttempt(clean, p.handle)
}

// EnqueuePrepared joins the source reservation transaction. The caller commits
// before exposing its receipt; failures must call CancelPrepared after rollback.
func (s *Service) EnqueuePrepared(ctx context.Context, tx *sql.Tx, p *PreparedInvocation) (Receipt, error) {
	if tx == nil || p == nil || p.owner != s || p.handle == "" {
		return Receipt{}, ErrDenied
	}
	j := p.job
	if _, err := tx.ExecContext(ctx, `UPDATE access_attempts SET completed_at=completed_at WHERE id=?`, j.Origin); err != nil {
		return Receipt{}, err
	}
	if err := s.checkJob(ctx, tx, j, p.handle); err != nil {
		return Receipt{}, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO restricted_workflow_jobs(id,workspace_id,principal_id,member_id,member_revision,pipeline_id,recipe_hash,recipe_json,agent_id,execution_profile,chat_id,origin_attempt_id,origin_handle_ciphertext,source_kind,page_id,page_action_json,page_spec_hash,inputs_json,idempotency_key_hash,state,created_at,fire_at,expires_at,additional_rights_json,source_facet,graph_json,graph_hash,proofs_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.Workspace, j.Principal, j.Member, j.Revision, j.Pipeline, j.RecipeHash, j.Recipe, j.Agent, j.Profile, j.Chat, j.Origin, j.Handle, j.Source, j.Page, j.PageAction, j.PageHash, j.Inputs, j.Idempotency, j.State, j.Created, j.FireAt, j.Expires, j.Rights, j.SourceFacet, j.Graph, j.GraphHash, j.Proofs)
	if err != nil {
		return Receipt{}, err
	}
	if err = s.freezeGraph(ctx, tx, j); err != nil {
		return Receipt{}, err
	}
	return Receipt{j.ID, j.Chat, "SCHEDULED"}, nil
}

// ReceiptForActor rechecks a private receipt without materializing its outputs.
func (s *Service) ReceiptForActor(ctx context.Context, user, workspace, id string) (Receipt, error) {
	j, err := s.load(ctx, id)
	if err != nil || j.Principal != user || j.Workspace != workspace {
		return Receipt{}, ErrDenied
	}
	handle, err := encryption.Decrypt(j.Handle)
	if err != nil || s.checkJob(ctx, s.db, j, handle) != nil {
		return Receipt{}, ErrDenied
	}
	if (access.Store{DB: s.db}).CheckContextAttempt(ctx, handle) != nil {
		return Receipt{}, ErrDenied
	}
	return Receipt{j.ID, j.Chat, j.State}, nil
}
