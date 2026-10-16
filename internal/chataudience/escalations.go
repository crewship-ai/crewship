package chataudience

import (
	"context"
	"database/sql"
)

// VerifiedTextReadSQL is the existing trusted chat audience, over an escalation
// aliased e. Only host-correlated TEXT sources acquire this additional fence;
// source-less service escalations and protected credential flows keep theirs.
const VerifiedTextReadSQL = `EXISTS (SELECT 1 FROM chats c JOIN workspaces source_workspace ON source_workspace.id=c.workspace_id AND source_workspace.deleted_at IS NULL JOIN agents source_agent ON source_agent.id=c.agent_id AND source_agent.workspace_id=c.workspace_id AND source_agent.deleted_at IS NULL WHERE e.deleted_at IS NULL AND c.id=e.chat_id AND c.workspace_id=e.workspace_id AND c.agent_id=e.from_agent_id
 AND EXISTS (SELECT 1 FROM journal_entries source_proof WHERE source_proof.workspace_id=e.workspace_id AND source_proof.trace_id=e.source_run_id AND source_proof.agent_id=e.from_agent_id AND source_proof.entry_type='run.started' AND json_valid(source_proof.payload) AND json_extract(source_proof.payload,'$.trigger_type')='USER' AND COALESCE(json_extract(source_proof.payload,'$.chat_origin_id'),'')='' AND json_extract(source_proof.payload,'$.chat_id')=c.id AND json_extract(source_proof.payload,'$.human_authority.workspace_id')=c.workspace_id AND json_extract(source_proof.payload,'$.human_authority.chat_id')=c.id AND json_extract(source_proof.payload,'$.human_authority.agent_id')=c.agent_id AND json_type(source_proof.payload,'$.human_authority.chat_generation')='text' AND length(json_extract(source_proof.payload,'$.human_authority.chat_generation'))>0 AND json_extract(source_proof.payload,'$.human_authority.chat_generation')=c.authority_generation AND (SELECT COUNT(*) FROM journal_entries unique_proof WHERE unique_proof.workspace_id=e.workspace_id AND unique_proof.trace_id=e.source_run_id AND unique_proof.entry_type='run.started')=1)
 AND NOT EXISTS (SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=? AND restricted.access_mode='restricted')
 AND EXISTS (SELECT 1 FROM workspace_members trusted JOIN users source_user ON source_user.id=trusted.user_id AND source_user.suspended_at IS NULL WHERE trusted.workspace_id=c.workspace_id AND trusted.user_id=? AND trusted.access_mode='trusted')
 AND (` + VisibleSQL + `))`

func VerifiedTextReadArgs(userID string) []any {
	return append([]any{userID, userID}, Args(userID)...)
}

// VerifiedTextSourceSQL identifies only sources whose correlation was validated
// by the existing host admission path, never an agent-chosen chat association.
const VerifiedTextSourceSQL = `e.type='TEXT' AND COALESCE(e.source_run_id,'')<>''`

type escalationQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// CanReadVerifiedEscalation preserves legacy source visibility while checking
// live chat access for a host-correlated TEXT source. It is usable inside the
// canonical source transaction before mutation and by notification delivery.
func CanReadVerifiedEscalation(ctx context.Context, db escalationQueryer, workspaceID, sourceID, userID string) (bool, error) {
	args := []any{workspaceID, sourceID}
	args = append(args, VerifiedTextReadArgs(userID)...)
	var allowed bool
	err := db.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM escalations e WHERE e.workspace_id=? AND e.id=? AND `+VerifiedTextSourceSQL+` AND NOT (`+VerifiedTextReadSQL+`))`, args...).Scan(&allowed)
	return allowed, err
}

// VerifiedTextJournalReadSQL applies the same source fence to its existing
// journal projection, before FTS/count/pagination can expose a hidden question.
const VerifiedTextJournalReadSQL = `NOT EXISTS (SELECT 1 FROM escalations e WHERE e.workspace_id=journal_entries.workspace_id AND e.id=CASE WHEN json_valid(journal_entries.refs) THEN json_extract(journal_entries.refs,'$.escalation_id') END AND ` + VerifiedTextSourceSQL + ` AND NOT (` + VerifiedTextReadSQL + `))`

func CanReadVerifiedEscalationJournal(ctx context.Context, db escalationQueryer, workspaceID, entryID, userID string) (bool, error) {
	var allowed bool
	err := db.QueryRowContext(ctx, `SELECT `+VerifiedTextJournalReadSQL+` FROM journal_entries WHERE workspace_id=? AND id=?`, append(VerifiedTextReadArgs(userID), workspaceID, entryID)...).Scan(&allowed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return allowed, err
}

const VerifiedTextInboxReadSQL = `NOT EXISTS (SELECT 1 FROM escalations e WHERE inbox_items.kind='escalation' AND e.id=inbox_items.source_id AND e.workspace_id=inbox_items.workspace_id AND ` + VerifiedTextSourceSQL + ` AND NOT (` + VerifiedTextReadSQL + `))`

func CanReadVerifiedEscalationInbox(ctx context.Context, db escalationQueryer, workspaceID, inboxID, userID string) (bool, error) {
	var allowed bool
	args := append(VerifiedTextReadArgs(userID), workspaceID, inboxID)
	err := db.QueryRowContext(ctx, `SELECT `+VerifiedTextInboxReadSQL+` FROM inbox_items WHERE workspace_id=? AND id=?`, args...).Scan(&allowed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return allowed, err
}
