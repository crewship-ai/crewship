package journal

import "context"

// Synchronous emit — for callers that must treat the audit write as a
// PRECONDITION of the action, not a side effect of it.
//
// Emit is deliberately asynchronous: it hands the entry to a batching worker
// and returns as soon as the queue accepts it. That is the right trade for
// observational events (an LLM call, a mission status change) — the hot path
// stays off the DB write lock and a lost entry costs visibility, not
// correctness.
//
// It is the wrong trade for a credential reveal (PRD-CREDENTIALS-V2-2026
// §2.6 L4). There the rule is "if the chained write fails, the reveal fails
// closed and the value is not returned" — and Emit cannot express that: it
// returns nil the moment the entry is buffered, long before persistBatch has
// run, and a later failure surfaces only as an ERROR line in the writer
// goroutine. Building the gate on Emit would mean returning the secret and
// discovering afterwards that nothing recorded it.
//
// Flush is not a substitute. Its barrier proves the worker has *drained* past
// the entry, but drain() retains a failed batch for retry and returns no error
// to the barrier, so Flush reports nil for an entry that never committed.
//
// EmitSync therefore writes inline, in the caller's goroutine, and returns the
// commit error. It shares prepareEntry and persistOne with the batch path, so
// the row it writes — including its link in the HMAC hash-chain — is identical
// to one that went through the queue.

// SyncEmitter is the write surface for audit-as-precondition call sites. It
// extends Emitter rather than replacing it so a handler holding a SyncEmitter
// can still emit ordinary observational entries asynchronously.
//
// Handlers depend on this interface (not on *Writer) so a test can substitute
// a recorder that fails on demand — proving the fail-closed branch is real
// rather than assumed.
type SyncEmitter interface {
	Emitter
	EmitSync(ctx context.Context, e Entry) (string, error)
}

// EmitSync persists the entry inline and returns only after it is durably
// committed — or returns the error that stopped it. The returned id is the
// entry's id, so the caller can reference the audit record it just created.
//
// Cost: one DB transaction on the caller's goroutine, serialized against the
// batch worker through writeMu (the hash-chain reads the workspace head then
// extends it, so two concurrent appends would race for the same seq). This is
// intentionally not something to reach for on a hot path — it exists for the
// handful of actions where an unrecorded success is worse than a failure.
//
// Compile-time assertion that *Writer satisfies SyncEmitter lives below, so
// dropping the method breaks the build rather than the reveal gate.
func (w *Writer) EmitSync(ctx context.Context, e Entry) (string, error) {
	e, err := prepareEntry(ctx, e)
	if err != nil {
		return "", err
	}
	if err := w.persistOne(ctx, e); err != nil {
		return "", err
	}
	// Same post-commit fan-out the batch path runs: wake SSE listeners and
	// hand the entry to the WebSocket / notify bridges. Skipping it would
	// make a synchronously-emitted entry invisible in the live feed until
	// the next unrelated write nudged the listeners.
	w.afterCommit([]Entry{e})
	return e.ID, nil
}

var _ SyncEmitter = (*Writer)(nil)
