package api

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

type invitationAuditContextKey struct{}
type invitationAuditBatch struct {
	emitter       journal.Emitter
	notifications []func()
}

func withInvitationAudit(ctx context.Context, emitter journal.Emitter) (context.Context, func()) {
	batch := &invitationAuditBatch{emitter: emitter}
	return context.WithValue(ctx, invitationAuditContextKey{}, batch), func() {
		for _, notify := range batch.notifications {
			notify()
		}
	}
}

// Only fixed, non-secret identifiers enter the journal. No proof, digest, email
// or request payload is copied into an audit entry.
func appendInvitationAudit(ctx context.Context, tx *sql.Tx, kind journal.EntryType, workspace, actor, id, role, subject string) error {
	e := journal.Entry{WorkspaceID: workspace, Type: kind, ActorType: journal.ActorUser, ActorID: actor, Summary: string(kind), Payload: map[string]any{"invitation_id": id, "role": role, "subject_user_id": subject}}
	batch, _ := ctx.Value(invitationAuditContextKey{}).(*invitationAuditBatch)
	appendTx := journal.AppendTx
	if batch != nil && batch.emitter != nil {
		emitter, ok := batch.emitter.(journal.TransactionEmitter)
		if !ok {
			return fmt.Errorf("invitation journal does not support atomic writes")
		}
		appendTx = emitter.AppendTx
	}
	notify, err := appendTx(ctx, tx, e)
	if err != nil {
		return err
	}
	if batch != nil {
		batch.notifications = append(batch.notifications, notify)
	}
	return nil
}
