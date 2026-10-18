package api

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Append on the caller's transaction, using the configured chain key. Unwired
// handlers still persist with the default key; unsupported emitters fail closed.
// The returned notification belongs strictly after the business commit.
func appendAuthorizationAudit(ctx context.Context, tx *sql.Tx, emitter journal.Emitter, entry journal.Entry) (func(), error) {
	switch writer := emitter.(type) {
	case nil, noopEmitter:
		return journal.AppendTx(ctx, tx, entry)
	case journal.TransactionEmitter:
		return writer.AppendTx(ctx, tx, entry)
	default:
		return nil, fmt.Errorf("authorization journal does not support atomic writes")
	}
}
