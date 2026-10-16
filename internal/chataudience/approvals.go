package chataudience

import (
	"context"
	"database/sql"
)

// VerifiedRunApprovalSourceSQL classifies host correlation independently of
// current access. Revocation and resource deletion must never turn a private
// source into a legacy approval. The caller aliases approvals_queue as aq.
const VerifiedRunApprovalSourceSQL = `COALESCE(CASE WHEN json_valid(aq.payload) THEN
 json_extract(aq.payload,'$.tool')='agent_run'
 AND json_extract(aq.payload,'$.gate_mode')='sync'
 AND json_extract(aq.payload,'$.correlation.trigger_type')='human_chat'
 AND COALESCE(json_extract(aq.payload,'$.correlation.chat_id'),'')<>''
 AND COALESCE(json_extract(aq.payload,'$.correlation.run_id'),'')<>''
 AND COALESCE(json_extract(aq.payload,'$.correlation.trigger_message_id'),'')<>''
 AND aq.kind NOT IN ('ephemeral_hire','autonomy_gate') ELSE 0 END,0)`

// VerifiedRunApprovalReadSQL uses the current reader's audience, while retaining
// the original resource generation. It does not bind the original human's
// membership: another currently authorized operator can review the source.
const runApprovalCurrentProofSQL = `c.id=json_extract(aq.payload,'$.correlation.chat_id') AND c.workspace_id=aq.workspace_id AND c.agent_id=aq.agent_id
 AND (SELECT COUNT(*) FROM journal_entries origin WHERE origin.workspace_id=aq.workspace_id AND origin.trace_id=json_extract(aq.payload,'$.correlation.run_id') AND origin.entry_type='run.started')=1
 AND EXISTS (SELECT 1 FROM journal_entries origin WHERE origin.workspace_id=aq.workspace_id AND origin.trace_id=json_extract(aq.payload,'$.correlation.run_id') AND origin.agent_id=aq.agent_id AND origin.entry_type='run.started' AND json_valid(origin.payload)
 AND json_extract(origin.payload,'$.trigger_type')='USER' AND COALESCE(json_extract(origin.payload,'$.chat_origin_id'),'')=''
 AND json_extract(origin.payload,'$.chat_id')=c.id AND json_extract(origin.payload,'$.trigger_message_id')=json_extract(aq.payload,'$.correlation.trigger_message_id')
 AND json_extract(origin.payload,'$.human_authority.workspace_id')=c.workspace_id AND json_extract(origin.payload,'$.human_authority.chat_id')=c.id AND json_extract(origin.payload,'$.human_authority.agent_id')=c.agent_id
 AND COALESCE(json_extract(origin.payload,'$.human_authority.chat_generation'),'')<>'' AND json_extract(origin.payload,'$.human_authority.chat_generation')=c.authority_generation) AND EXISTS (SELECT 1 FROM workspaces source_workspace WHERE source_workspace.id=c.workspace_id AND source_workspace.deleted_at IS NULL)
 AND EXISTS (SELECT 1 FROM agents source_agent WHERE source_agent.id=c.agent_id AND source_agent.workspace_id=c.workspace_id AND source_agent.deleted_at IS NULL)`

const VerifiedRunApprovalReadSQL = `CASE WHEN json_valid(aq.payload) THEN EXISTS (
 SELECT 1 FROM chats c WHERE (` + runApprovalCurrentProofSQL + `)
 AND NOT EXISTS (SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=? AND restricted.access_mode='restricted')
 AND EXISTS (SELECT 1 FROM workspace_members trusted JOIN users source_user ON source_user.id=trusted.user_id AND source_user.suspended_at IS NULL WHERE trusted.workspace_id=c.workspace_id AND trusted.user_id=? AND trusted.access_mode='trusted')
 AND (` + VisibleSQL + `)) ELSE 0 END`

func VerifiedRunApprovalReadArgs(userID string) []any {
	return append([]any{userID, userID}, Args(userID)...)
}

const RunApprovalVisibilitySQL = `NOT COALESCE((` + VerifiedRunApprovalSourceSQL + `),0) OR (` + VerifiedRunApprovalReadSQL + `)`

func CanReadRunApproval(ctx context.Context, db Queryer, workspace, id, user string) (bool, error) {
	var allowed bool
	args := append(VerifiedRunApprovalReadArgs(user), workspace, id)
	err := db.QueryRowContext(ctx, `SELECT (`+RunApprovalVisibilitySQL+`) FROM approvals_queue aq WHERE aq.workspace_id=? AND aq.id=?`, args...).Scan(&allowed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return allowed, err
}

// RetiredRunApprovalSourceSQL is over journal_entries. Only host system events
// can retire classification; the agent journal ingestion allowlist excludes
// this type and fixes its actor to sidecar. The marker contains no source text.
const RetiredRunApprovalSourceSQL = `EXISTS (SELECT 1 FROM journal_entries retired WHERE retired.workspace_id=journal_entries.workspace_id AND retired.entry_type='approval.source_retired' AND retired.actor_type='system' AND json_valid(retired.refs) AND json_extract(retired.refs,'$.approval_id')=CASE WHEN json_valid(journal_entries.refs) THEN json_extract(journal_entries.refs,'$.approval_id') END)`

// RunApprovalJournalSource supports suppression on broad workspace streams.
// Classification survives deleted chats, generations, members and source rows.
func RunApprovalJournalSource(ctx context.Context, db Queryer, workspace, id string) (bool, string, error) {
	var retired bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM journal_entries WHERE workspace_id=? AND entry_type='approval.source_retired' AND actor_type='system' AND json_valid(refs) AND json_extract(refs,'$.approval_id')=?)`, workspace, id).Scan(&retired)
	if err != nil || retired {
		return retired, "", err
	}
	var supported bool
	var chat string
	err = db.QueryRowContext(ctx, `SELECT (`+VerifiedRunApprovalSourceSQL+`),COALESCE((SELECT c.id FROM chats c WHERE (`+runApprovalCurrentProofSQL+`) LIMIT 1),'') FROM approvals_queue aq WHERE aq.workspace_id=? AND aq.id=? AND json_valid(aq.payload)`, workspace, id).Scan(&supported, &chat)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if !supported {
		chat = ""
	}
	return supported, chat, err
}
