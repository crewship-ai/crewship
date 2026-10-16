package api

import (
	"context"
	"errors"

	"github.com/crewship-ai/crewship/internal/journal"
)

type sourceClassificationContext struct{ context.Context }

func (sourceClassificationContext) Value(any) any { return nil }

// Only validated direct-human TEXT sources acquire durable classification. The
// existing source row remains authoritative; this marker prevents an absent
// ephemeral source from making retained projections legacy-public after restore.
func (h *QueryHandler) insertEscalationSource(ctx context.Context, workspace, id string, classified bool, query string, args []any) error {
	if !classified {
		_, err := h.db.ExecContext(ctx, query, args...)
		return err
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	appendTx := journal.AppendTx
	_, unconfigured := h.journal.(noopEmitter)
	if h.journal != nil && !unconfigured {
		writer, ok := h.journal.(journal.TransactionEmitter)
		if !ok {
			return errors.New("source classification journal cannot write atomically")
		}
		appendTx = writer.AppendTx
	}
	notify, err := appendTx(sourceClassificationContext{ctx}, tx, journal.Entry{WorkspaceID: workspace, Type: journal.EntryEscalationSourceClassified, ActorType: journal.ActorSystem, Summary: "Private question source classified", Payload: map[string]any{}, Refs: map[string]any{"escalation_id": id}})
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if notify != nil {
		notify()
	}
	return nil
}
