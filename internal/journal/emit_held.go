package journal

import (
	"context"
	"errors"
	"time"
)

// A broken chain tail is not poison. It refuses every entry for one workspace
// until an OWNER reseals (or the tail is repaired), and dropping those entries
// would lose exactly the activity an investigation needs. The background
// writer therefore HOLDS them: per workspace, in order, retried every
// HeldRetryInterval with one tail-only admission per workspace, and bounded by
// maxHeldEntries (oldest dropped loudly). Other workspaces keep committing.

// maxHeldEntries bounds the entries held for broken chains (~2.5 MB at the
// batch path's 256 B estimate). Far beyond any reseal turnaround of a busy
// install; past it the oldest are dropped with an ERROR naming the count.
const maxHeldEntries = 10000

const defaultHeldRetryInterval = 30 * time.Second

// chainBreakReportInterval rate-limits the observer per workspace: a broken
// tail refuses every write, and one report per minute is enough to alert on.
const chainBreakReportInterval = time.Minute

// isChainAdmissionError reports a refusal by the chain tail (or its key).
func isChainAdmissionError(err error) bool {
	return errors.Is(err, ErrChainIntegrity) || errors.Is(err, ErrSigningKeyUnavailable)
}

// SetChainBreakObserver registers fn to hear when an append is refused
// because a workspace chain tail does not authenticate. It runs inline on the
// write path (possibly inside a caller's transaction), so it must not block or
// write to the database itself; the API's alerter hands off to a goroutine.
// The context carries this writer's signing-key snapshot.
func (w *Writer) SetChainBreakObserver(fn func(ctx context.Context, workspaceID string, err error)) {
	w.chainBreakMu.Lock()
	w.chainBreakObs = fn
	w.chainBreakMu.Unlock()
}

func (w *Writer) reportChainBreak(workspaceID string, err error) {
	w.chainBreakMu.Lock()
	fn := w.chainBreakObs
	if fn == nil || time.Since(w.chainBreakAt[workspaceID]) < chainBreakReportInterval {
		w.chainBreakMu.Unlock()
		return
	}
	if w.chainBreakAt == nil {
		w.chainBreakAt = map[string]time.Time{}
	}
	w.chainBreakAt[workspaceID] = time.Now()
	w.chainBreakMu.Unlock()
	w.logger.Error("journal chain tail does not authenticate; appends to this workspace are refused until reseal", "workspace_id", workspaceID, "err", err)
	fn(WithSigningKeys(context.Background(), w.signingKeys), workspaceID, err)
}

// groupByWorkspace splits a batch into per-workspace runs, keeping order.
func groupByWorkspace(batch []Entry) [][]Entry {
	index := map[string]int{}
	var groups [][]Entry
	for _, e := range batch {
		i, ok := index[e.WorkspaceID]
		if !ok {
			i = len(groups)
			index[e.WorkspaceID] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], e)
	}
	return groups
}

// hold appends entries to held and enforces maxHeldEntries.
func (w *Writer) hold(held, entries []Entry) []Entry {
	held = append(held, entries...)
	if over := len(held) - maxHeldEntries; over > 0 {
		w.logger.Error("journal entries held by a broken audit chain exceeded the cap — dropping oldest", "dropped", over, "held", maxHeldEntries)
		held = append(held[:0:0], held[over:]...)
	}
	return held
}

// retryHeld attempts each held workspace once, as one batch. It returns what
// is still held and what committed.
func (w *Writer) retryHeld(held []Entry) (kept, committed []Entry) {
	for _, group := range groupByWorkspace(held) {
		err := w.persistBatch(context.Background(), group)
		switch {
		case err == nil:
			committed = append(committed, group...)
		case isChainAdmissionError(err) || !isPermanentDBError(err):
			kept = append(kept, group...)
		default:
			// Persisted alone, so poison is dropped per entry as on the batch path.
			for _, e := range group {
				perr := w.persistOne(context.Background(), e)
				switch {
				case perr == nil:
					committed = append(committed, e)
				case isChainAdmissionError(perr) || !isPermanentDBError(perr):
					kept = append(kept, e)
				default:
					w.logger.Error("held journal entry permanently rejected — dropping", "err", perr, "entry_id", e.ID, "entry_type", e.Type, "workspace_id", e.WorkspaceID)
				}
			}
		}
	}
	return kept, committed
}
