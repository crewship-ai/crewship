package api

import (
	"context"
	"database/sql"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// A source hint is itself observable activity. Recheck the same whole-proposal
// visibility predicate as list/show after commit; an inaccessible source emits
// nothing. Trusted chat delivery still rechecks each subscriber's live audience.
func broadcastProposalInvalidation(ctx context.Context, db *sql.DB, hub *ws.Hub, workspace, id string) {
	if db == nil || hub == nil || workspace == "" || id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return
	}
	defer tx.Rollback()
	var chat string
	if err = tx.QueryRowContext(ctx, `SELECT chat_id FROM chat_issue_proposals WHERE workspace_id=? AND id=? AND `+proposalVisibleSQL, workspace, id).Scan(&chat); err != nil {
		return
	}
	p, err := loadProposal(ctx, tx, chat, id)
	if err != nil || p.Workspace != workspace || validateE2Card(p.response()) != nil {
		return
	}
	if err = tx.Commit(); err != nil {
		return
	}
	broadcastChatEntityRefsInvalidation(ctx, hub, chat)
}
