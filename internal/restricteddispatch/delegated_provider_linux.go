package restricteddispatch

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
)

// ProviderDelegationHash is a trusted planning API. It reveals neither the key
// nor the grant identity to a model, and never selects a workspace fallback.
func (a Authority) ProviderDelegationHash(ctx context.Context, user, workspace, agent string) (string, error) {
	if err := a.Store.Check(ctx, user, workspace, access.Right{Kind: "agent", ID: agent, Operation: "run"}); err != nil {
		return "", err
	}
	s, err := providerCurrent(ctx, a.Store.DB, access.Attempt{Workspace: workspace, Agent: agent}, "")
	if err != nil || !delegatedProfile(s.Profile) {
		return "", access.ErrDenied
	}
	return delegationPolicyHash(s), nil
}

func delegatedProfile(profile string) bool {
	return profile == "responses_text" || profile == "native_api_key"
}

func delegationPolicyHash(s providerSnapshot) string {
	raw, _ := json.Marshal([]string{s.Credential, s.Grant, s.revision(), s.Model, s.Profile, s.GrantExpiry.String, s.KeyExpiry.String})
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func hasAgentRight(rights []access.Right, agent, operation string) bool {
	for _, r := range rights {
		if r.Kind == "agent" && r.ID == agent && r.Operation == operation {
			return true
		}
	}
	return false
}

// CheckWorkflowProvider validates the frozen policy even after a leaf has
// completed. Expiry and ambiguity cannot silently change a queued declaration.
func (a Authority) CheckWorkflowProvider(ctx context.Context, q providerQuery, jobID, agent, expectedHash string, limit int64) error {
	if q == nil || expectedHash == "" || limit < 1 || limit > 32768 {
		return access.ErrDenied
	}
	var workspace, expires, state string
	err := q.QueryRowContext(ctx, `SELECT j.workspace_id,j.expires_at,j.state FROM restricted_workflow_jobs j
 JOIN restricted_workflow_provider_policies p ON p.job_id=j.id AND p.agent_id=?
 JOIN access_attempts origin ON origin.id=j.origin_attempt_id
 JOIN workspace_members m ON m.id=j.member_id AND m.access_revision=j.member_revision AND m.access_mode='restricted'
 WHERE j.id=? AND j.state IN ('pending','running','completed') AND origin.revoked_at IS NULL AND p.policy_hash=? AND p.max_output_tokens>=?`, agent, jobID, expectedHash, limit).Scan(&workspace, &expires, &state)
	if err != nil {
		return access.ErrDenied
	}
	deadline, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || (state != "completed" && !deadline.After(time.Now())) {
		return access.ErrDenied
	}
	s, err := providerCurrent(ctx, q, access.Attempt{Workspace: workspace, Agent: agent}, "")
	if err != nil || !delegatedProfile(s.Profile) || delegationPolicyHash(s) != expectedHash {
		return access.ErrDenied
	}
	return nil
}

// FreezeWorkflowProvider joins the immutable queue insertion transaction. The
// completed origin must already capture run and, for a foreign agent, delegate.
func (a Authority) FreezeWorkflowProvider(ctx context.Context, tx *sql.Tx, jobID, agent, expectedHash string, limit int64) error {
	if tx == nil || expectedHash == "" || limit < 1 || limit > 32768 {
		return access.ErrDenied
	}
	var origin access.Attempt
	var rights, state, expires string
	err := tx.QueryRowContext(ctx, `SELECT a.workspace_id,a.principal_id,a.agent_id,a.rights,j.state,j.expires_at
 FROM restricted_workflow_jobs j JOIN access_attempts a ON a.id=j.origin_attempt_id
 JOIN workspace_members m ON m.id=a.member_id AND m.user_id=a.principal_id AND m.workspace_id=a.workspace_id AND m.access_revision=a.member_revision AND m.access_mode='restricted'
 WHERE j.id=? AND j.workspace_id=a.workspace_id AND j.principal_id=a.principal_id AND j.member_id=a.member_id AND j.member_revision=a.member_revision
 AND j.chat_id=a.chat_id AND a.admission_operation='run' AND a.context_audience='' AND a.completed_at IS NOT NULL AND a.revoked_at IS NULL`, jobID).Scan(&origin.Workspace, &origin.Principal, &origin.Agent, &rights, &state, &expires)
	if err != nil || state != "pending" || json.Unmarshal([]byte(rights), &origin.Rights) != nil || !hasAgentRight(origin.Rights, agent, "run") || (agent != origin.Agent && !hasAgentRight(origin.Rights, agent, "delegate")) {
		return access.ErrDenied
	}
	deadline, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !deadline.After(time.Now()) {
		return access.ErrDenied
	}
	origin.Agent = agent
	s, err := providerCurrent(ctx, tx, origin, "")
	if err != nil || !delegatedProfile(s.Profile) || delegationPolicyHash(s) != expectedHash {
		return access.ErrDenied
	}
	providerExpiry, err := s.deadline(time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC))
	if err != nil {
		return access.ErrDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO restricted_workflow_provider_policies(job_id,agent_id,credential_id,grant_id,policy_hash,max_output_tokens,expires_at) VALUES(?,?,?,?,?,?,?)`, jobID, agent, s.Credential, s.Grant, expectedHash, limit, providerExpiry.UTC().Format(time.RFC3339Nano))
	return err
}

// BindProviderDelegation attaches a frozen target slot to the exact active
// orchestration parent. Building its scoped context must first bind the durable
// workflow origin, so an unrelated admitted handle cannot consume this job.
func (a Authority) BindProviderDelegation(ctx context.Context, parentHandle, jobID, agent, expectedHash string, limit int64) error {
	p, err := a.Store.Resolve(ctx, parentHandle)
	if err != nil || p.AdmissionOperation != "run" || p.ContextAudience != "" || p.Parent != "" || !hasAgentRight(p.Rights, agent, "run") || (agent != p.Agent && !hasAgentRight(p.Rights, agent, "delegate")) {
		return access.ErrDenied
	}
	tx, err := a.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := a.CheckWorkflowProvider(ctx, tx, jobID, agent, expectedHash, limit); err != nil {
		return err
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM restricted_workflow_jobs j
 JOIN access_attempts origin ON origin.id=j.origin_attempt_id
 JOIN access_context_dependencies d ON d.attempt_id=? AND d.source_attempt_id=origin.id
 JOIN restricted_workflow_provider_policies policy ON policy.job_id=j.id AND policy.agent_id=?
 WHERE j.id=? AND j.state='running' AND j.principal_id=? AND j.workspace_id=? AND j.member_id=? AND j.member_revision=? AND j.chat_id=? AND j.agent_id=?
 AND origin.revoked_at IS NULL AND policy.policy_hash=? AND policy.max_output_tokens>=?`, p.ID, agent, jobID, p.Principal, p.Workspace, p.Member, p.Revision, p.Chat, p.Agent, expectedHash, limit).Scan(&one)
	if err != nil || limit < 1 || limit > 32768 {
		return access.ErrDenied
	}
	s, err := providerCurrent(ctx, tx, access.Attempt{Workspace: p.Workspace, Agent: agent}, "")
	if err != nil || delegationPolicyHash(s) != expectedHash {
		return access.ErrDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO restricted_workflow_delegate_slots(parent_attempt_id,job_id,agent_id,policy_hash,max_output_tokens) VALUES(?,?,?,?,?)`, p.ID, jobID, agent, expectedHash, limit)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_, err = a.Store.Resolve(ctx, parentHandle)
	return err
}

// PrepareDelegatedResponses uses only a server-bound target provider slot.
// Ordinary PrepareResponses continues to enforce inherited provider identity.
func (a Authority) PrepareDelegatedResponses(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, limit int64, build BoundBuildCommand) (string, access.Attempt, error) {
	if parent == "" || build == nil {
		return "", access.Attempt{}, access.ErrDenied
	}
	return a.prepareDelegatedBound(ctx, user, workspace, agent, chat, parent, rights, func(ctx context.Context, handle string, child access.Attempt) ([]string, error) {
		if err := a.pinDelegatedProvider(ctx, child, limit, "responses_text"); err != nil {
			return nil, err
		}
		return build(ctx, handle, child)
	})
}

func (a Authority) prepareDelegatedBound(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, build BoundBuildCommand) (string, access.Attempt, error) {
	p, err := a.Store.Resolve(ctx, parent)
	if err != nil || p.AdmissionOperation != "run" || p.Principal != user || p.Workspace != workspace || p.Chat != chat {
		return "", access.Attempt{}, access.ErrDenied
	}
	return a.prepareBoundAdmission(ctx, user, workspace, agent, chat, parent, rights, build, false, p.Agent == agent)
}

func (a Authority) pinDelegatedProvider(ctx context.Context, child access.Attempt, limit int64, profile string) error {
	if child.Parent == "" || limit < 1 || limit > 32768 || !delegatedProfile(profile) {
		return access.ErrDenied
	}
	tx, err := a.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var expected string
	err = tx.QueryRowContext(ctx, `SELECT s.policy_hash FROM restricted_workflow_delegate_slots s
 JOIN restricted_workflow_jobs j ON j.id=s.job_id JOIN access_attempts origin ON origin.id=j.origin_attempt_id
 WHERE s.parent_attempt_id=? AND s.agent_id=? AND s.max_output_tokens>=? AND j.state='running'
 AND j.workspace_id=? AND j.principal_id=? AND j.member_id=? AND j.member_revision=? AND j.chat_id=? AND origin.revoked_at IS NULL`, child.Parent, child.Agent, limit, child.Workspace, child.Principal, child.Member, child.Revision, child.Chat).Scan(&expected)
	if err != nil {
		return access.ErrDenied
	}
	s, err := providerCurrent(ctx, tx, child, "")
	if err != nil || s.Profile != profile || delegationPolicyHash(s) != expected {
		return access.ErrDenied
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO restricted_provider_bindings(attempt_id,credential_id,grant_id,credential_revision,model,max_output_tokens,execution_profile) VALUES(?,?,?,?,?,?,?)`, child.ID, s.Credential, s.Grant, s.revision(), s.Model, limit, s.Profile)
	if err != nil {
		return err
	}
	return tx.Commit()
}
