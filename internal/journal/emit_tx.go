package journal

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
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
	if err := w.persistBatchTx(ctx, tx, []Entry{e}, true); err != nil {
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
	if err = w.persistBatchTx(ctx, tx, []Entry{e}, true); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Only synchronous transactional append consumers request this native readback.
// Background batching keeps its original query path. The expected tuple is the
// exact INSERT argument vector, including the HMAC made with w.chainKey; never
// derive another key or bless a row merely because rereading it succeeded.
func acknowledgeTxInsert(ctx context.Context, tx *sql.Tx, expected []any) error {
	actual := make([]any, len(expected))
	dest := make([]any, len(expected))
	for i := range actual {
		dest[i] = &actual[i]
	}
	if err := tx.QueryRowContext(ctx, `SELECT id,workspace_id,crew_id,agent_id,mission_id,ts,entry_type,severity,priority,actor_type,actor_id,summary,payload,refs,trace_id,span_id,expires_at,seq,prev_hash,entry_hash,priority_at_emit FROM journal_entries WHERE id=?`, expected[0]).Scan(dest...); err != nil {
		return fmt.Errorf("journal: acknowledge transactional row: %w", err)
	}
	for i, v := range expected {
		native, err := driver.DefaultParameterConverter.ConvertValue(v)
		if err != nil {
			return fmt.Errorf("journal: canonical argument %d: %w", i, err)
		}
		if !reflect.DeepEqual(actual[i], native) {
			return fmt.Errorf("journal: transactional append changed stored field %d", i)
		}
	}
	return nil
}
