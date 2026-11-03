package api

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

// The last_used_at breadcrumb for CLI tokens (#4770, #4847).
//
// It is a UX hint on the settings page, shown to the minute, so it is written
// at most once per token per interval and never on the request path. #4788
// moved the write onto the caller with a 250 ms context: with another writer
// holding the lock, every due request, read-only ones included, waited out that
// context (longer when the pool had to open a connection), and the touch was
// then dropped. A context is also not a reliable bound on SQLite's lock wait in
// general (see database.WithBusyTimeout). Authentication now only records the
// touch in memory; one worker
// per database writes the queued touches after cliTokenTouchDelay and is the
// only goroutine that ever waits for the lock.
//
// Bounds: one worker per *sql.DB with pending touches (it exits once its queue
// is empty), at most cap pending touches in total, coalesced per token, and a
// throttle map of at most cap entries. A touch that does not fit is not
// recorded in the throttle map, so the next use of that token tries again.
//
// The delay is what keeps #4770 fixed: a worker that wrote immediately would
// race the handler's own queries, and the pool would open (and schema-parse) a
// second SQLite connection for a breadcrumb. After the delay the handler's
// connection is normally idle and is reused.
//
// Not lost in the normal path: a failed batch is retried by the next use after
// cliTokenTouchRetry, and FlushCLITokenTouches writes what is queued at
// shutdown, before the database is closed.

const (
	// cliTokenTouchInterval bounds how often one process writes a token's
	// last_used_at.
	cliTokenTouchInterval = time.Minute
	// cliTokenTouchRetry is how soon a token whose write failed may be queued
	// again, so a token used once while the database was busy still gets its
	// breadcrumb.
	cliTokenTouchRetry = 5 * time.Second
	// cliTokenTouchDelay is how long a worker waits before writing its queue.
	cliTokenTouchDelay = time.Second
	// cliTokenTouchCap bounds both the throttle map and the pending queue.
	cliTokenTouchCap = 4096
	// cliTokenTouchWriteTimeout bounds a batch write. The busy wait inside it is
	// bounded by busy_timeout, not by this; it is a backstop for the pool wait.
	cliTokenTouchWriteTimeout = time.Minute
)

type cliTokenToucher struct {
	interval, retry, delay time.Duration
	cap                    int

	mu      sync.Mutex
	next    map[string]time.Time // token ID -> earliest time it may be queued again
	queues  map[*sql.DB]*cliTokenTouchQueue
	pending int
	// written counts successful batch writes; tests read it.
	written int
}

type cliTokenTouchQueue struct {
	tokens  map[string]string // token ID -> last_used_at value
	flush   chan struct{}     // closed to skip the delay
	flushed bool
	done    chan struct{} // closed when the worker exits
}

func newCLITokenToucher(interval, retry, delay time.Duration, capacity int) *cliTokenToucher {
	return &cliTokenToucher{
		interval: interval, retry: retry, delay: delay, cap: capacity,
		next:   map[string]time.Time{},
		queues: map[*sql.DB]*cliTokenTouchQueue{},
	}
}

var cliTokenTouches = newCLITokenToucher(cliTokenTouchInterval, cliTokenTouchRetry, cliTokenTouchDelay, cliTokenTouchCap)

// touch queues tokenID's last_used_at = nowStr on db and returns at once. It
// never touches the database.
func (w *cliTokenToucher) touch(db *sql.DB, tokenID, nowStr string) {
	now := time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if next, ok := w.next[tokenID]; ok && now.Before(next) {
		return
	}
	q := w.queues[db]
	queued := false
	if q != nil {
		_, queued = q.tokens[tokenID]
	}
	if !queued && w.pending >= w.cap {
		return // full; not throttled, so the next use tries again
	}
	if len(w.next) >= w.cap {
		for id, next := range w.next {
			if !now.Before(next) {
				delete(w.next, id)
			}
		}
		if len(w.next) >= w.cap {
			// Every entry is recent: clearing costs at most one extra
			// write per token.
			clear(w.next)
		}
	}
	w.next[tokenID] = now.Add(w.interval)
	if q == nil {
		q = &cliTokenTouchQueue{tokens: map[string]string{}, flush: make(chan struct{}), done: make(chan struct{})}
		w.queues[db] = q
		go w.run(db, q)
	}
	if !queued {
		w.pending++
	}
	q.tokens[tokenID] = nowStr
}

// run writes q's touches until q is empty, then removes q and exits. Only this
// goroutine waits for db's writer lock.
func (w *cliTokenToucher) run(db *sql.DB, q *cliTokenTouchQueue) {
	defer close(q.done)
	timer := time.NewTimer(w.delay)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-q.flush:
		}
		w.mu.Lock()
		batch := q.tokens
		q.tokens = map[string]string{}
		w.pending -= len(batch)
		w.mu.Unlock()

		err := writeCLITokenTouches(db, batch)

		w.mu.Lock()
		if err != nil {
			// Best-effort and silent, but retried soon rather than in an
			// interval. Only lower the deadline: a newer touch may already
			// have set its own.
			retryAt := time.Now().Add(w.retry)
			for id := range batch {
				if next, ok := w.next[id]; !ok || next.After(retryAt) {
					w.next[id] = retryAt
				}
			}
		} else {
			w.written++
		}
		if len(q.tokens) == 0 {
			delete(w.queues, db)
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		timer.Reset(w.delay)
	}
}

func writeCLITokenTouches(db *sql.DB, batch map[string]string) error {
	if len(batch) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cliTokenTouchWriteTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for id, ts := range batch {
		if _, err := tx.ExecContext(ctx, "UPDATE cli_tokens SET last_used_at = ? WHERE id = ?", ts, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// flush makes every current worker write now and waits for them to exit, or
// for ctx. It reports whether they all exited.
func (w *cliTokenToucher) flush(ctx context.Context) bool {
	w.mu.Lock()
	done := make([]chan struct{}, 0, len(w.queues))
	for _, q := range w.queues {
		if !q.flushed {
			q.flushed = true
			close(q.flush)
		}
		done = append(done, q.done)
	}
	w.mu.Unlock()
	for _, d := range done {
		select {
		case <-d:
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// FlushCLITokenTouches writes the queued CLI token last_used_at touches and
// waits for the writers to exit, or for ctx. Server shutdown calls it before
// closing the database so queued breadcrumbs are not dropped.
func FlushCLITokenTouches(ctx context.Context) bool {
	return cliTokenTouches.flush(ctx)
}
