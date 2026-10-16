package harbormaster

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Keep cancellation/deadline semantics without inheriting run or trace values
// into this deliberately content-free classification marker.
type retirementContext struct{ context.Context }

func (retirementContext) Value(any) any { return nil }

// DeleteSources keeps the existing hard-delete contract. Only verified private
// sources leave a content-free immutable journal marker in the same transaction
// so their retained history never becomes legacy-public after source removal.
// Predicate is host-owned SQL over approvals_queue aq, never client input.
func DeleteSources(ctx context.Context, db *sql.DB, writer journal.Emitter, workspace, predicate string, args ...any) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	bindings := append([]any{workspace}, args...)
	where := `aq.workspace_id=? AND (` + predicate + `)`
	rows, err := tx.QueryContext(ctx, `SELECT aq.id FROM approvals_queue aq WHERE `+where+` AND (`+chataudience.VerifiedRunApprovalSourceSQL+`)`, bindings...)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	appendTx := journal.AppendTx
	if writer != nil && len(ids) > 0 {
		configured, ok := writer.(journal.TransactionEmitter)
		if !ok {
			return 0, errors.New("approval retirement journal cannot write atomically")
		}
		appendTx = configured.AppendTx
	}
	var callbacks []func()
	for _, id := range ids {
		notify, err := appendTx(retirementContext{ctx}, tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryApprovalSourceRetired, ActorType: journal.ActorSystem, Summary: "Private approval source retired", Payload: map[string]any{}, Refs: map[string]any{"approval_id": id}})
		if err != nil {
			return 0, err
		}
		callbacks = append(callbacks, notify)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM approvals_queue AS aq WHERE `+where, bindings...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	for _, notify := range callbacks {
		if notify != nil {
			notify()
		}
	}
	return n, nil
}
