package chataudience

import (
	"context"
	"database/sql"
)

// This is a projection of existing source audiences, not a new grant. Retired
// sources keep their host-written classification after canonical hard deletion.
const DecisionJournalReadSQL = `(` + VerifiedTextJournalReadSQL + `) AND NOT (` + RetiredRunApprovalSourceSQL + `) AND NOT (journal_entries.entry_type='approval.source_retired' AND journal_entries.actor_type='system') AND NOT EXISTS (SELECT 1 FROM approvals_queue aq WHERE aq.workspace_id=journal_entries.workspace_id AND aq.id=CASE WHEN json_valid(journal_entries.refs) THEN json_extract(journal_entries.refs,'$.approval_id') END AND (` + VerifiedRunApprovalSourceSQL + `) AND NOT (` + VerifiedRunApprovalReadSQL + `))`

func DecisionJournalReadArgs(user string) []any {
	return append(VerifiedTextReadArgs(user), VerifiedRunApprovalReadArgs(user)...)
}

func CanReadDecisionJournal(ctx context.Context, db Queryer, workspace, id, user string) (bool, error) {
	var allowed bool
	args := append(DecisionJournalReadArgs(user), workspace, id)
	err := db.QueryRowContext(ctx, `SELECT `+DecisionJournalReadSQL+` FROM journal_entries WHERE workspace_id=? AND id=?`, args...).Scan(&allowed)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return allowed, err
}
