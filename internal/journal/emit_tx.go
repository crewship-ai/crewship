package journal

import (
	"context"
	"database/sql"
)

// TransactionEmitter appends using the configured writer's chain key. The
// returned notification must run only after the caller successfully commits.
// The transaction must use the writer's database and serialize writes (the
// production SQLite connection uses immediate write transactions). Do not hold
// writeMu here: a worker may already hold it while waiting for this transaction.
type TransactionEmitter interface {
	AppendTx(context.Context, *sql.Tx, Entry) (func(), error)
}

func (w *Writer) AppendTx(ctx context.Context, tx *sql.Tx, e Entry) (func(), error) {
	e, err := prepareEntry(ctx, e)
	if err != nil {
		return nil, err
	}
	if err := w.persistBatchTx(ctx, tx, []Entry{e}); err != nil {
		return nil, err
	}
	return func() { w.afterCommit([]Entry{e}) }, nil
}

// AppendTx is the standalone default-key variant, for handlers without a
// configured journal writer. Configured writers must use their method instead.
// Its durable entry is discoverable by polling; no observer is configured here.
func AppendTx(ctx context.Context, tx *sql.Tx, e Entry) (func(), error) {
	w := &Writer{chainKey: ChainKeyFromEnv()}
	e, err := prepareEntry(ctx, e)
	if err != nil {
		return nil, err
	}
	if err = w.persistBatchTx(ctx, tx, []Entry{e}); err != nil {
		return nil, err
	}
	return func() {}, nil
}
