package api

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func readCLITokenLastUsed(t *testing.T, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var lastUsed sql.NullString
	if err := db.QueryRow(`SELECT last_used_at FROM cli_tokens WHERE id = ?`, id).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	return lastUsed
}

func seedStandardCLIToken(t *testing.T, db *sql.DB, label string) (id, token string) {
	t.Helper()
	var userID string
	if err := db.QueryRow(`SELECT id FROM users LIMIT 1`).Scan(&userID); err == sql.ErrNoRows {
		userID = seedTestUser(t, db)
	} else if err != nil {
		t.Fatal(err)
	}
	id = label + "-" + generateCUID()
	token = cliTokenStandardPrefix + label + id
	execOrFatal(t, db, `INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, created_at)
		VALUES (?, ?, ?, ?, 'STANDARD', datetime('now'))`, id, userID, label, hashStandard(token))
	return id, token
}

func waitCLITokenLastUsed(t *testing.T, db *sql.DB, id string, want func(sql.NullString) bool) sql.NullString {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := readCLITokenLastUsed(t, db, id)
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("last_used_at for %s still %+v after 15s", id, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// #4847: with another writer holding SQLite's lock, authenticating a STANDARD
// token that is due for its last_used_at touch must not wait for that lock,
// and the touch must still land once the lock is released. #4788 wrote the
// touch on the caller under a 250 ms context: every due request with the lock
// held paid that wait (more when the pool had to open a connection), and the
// touch then failed and was dropped until the token's next use.
func TestValidateCLITokenDoesNotWaitForAHeldWriterLock(t *testing.T) {
	// Not parallel: the bound is about the touch, not about CPU contention.
	const bound = 200 * time.Millisecond
	db := setupTestDB(t)
	id, token := seedStandardCLIToken(t, db, "locked")

	ctx := context.Background()
	// Warm a second pooled connection first, so the read below does not pay
	// for opening one (a schema parse, slow under -race) inside the bound.
	warm, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = warm.Close()
	if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			_, _ = holder.ExecContext(ctx, "ROLLBACK")
			_ = holder.Close()
		}
	}
	defer release()

	type result struct {
		res ValidateCLITokenResult
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		res, err := ValidateCLITokenFull(ctx, db, token, ValidateAuditContext{})
		done <- result{res, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("validate: %v", r.err)
		}
		if r.res.TokenID != id {
			t.Fatalf("validated token %q, want %q", r.res.TokenID, id)
		}
		if took := time.Since(start); took >= bound {
			t.Fatalf("authentication took %v with the writer lock held, want < %v", took, bound)
		}
	case <-time.After(bound):
		r := <-done // the lock is still held
		t.Fatalf("authentication waited on the held writer lock: no result within %v (returned after %v, err=%v)", bound, time.Since(start).Round(time.Millisecond), r.err)
	}

	release()
	waitCLITokenLastUsed(t, db, id, func(v sql.NullString) bool { return v.Valid })
}

func flushToucher(t *testing.T, w *cliTokenToucher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !w.flush(ctx) {
		t.Fatal("touch workers did not exit within 20s")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queues) != 0 || w.pending != 0 {
		t.Fatalf("after flush: %d queues, %d pending, want none", len(w.queues), w.pending)
	}
}

// #4770 stays fixed: the queued touch reuses the handler's idle connection
// instead of opening (and schema-parsing) a second one, and validation does
// not write before it returns.
func TestValidateCLITokenQueuesTheTouchAndReusesTheConnection(t *testing.T) {
	db := setupTestDB(t)
	id, token := seedStandardCLIToken(t, db, "touch")
	if _, err := ValidateCLITokenFull(context.Background(), db, token, ValidateAuditContext{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !FlushCLITokenTouches(ctx) {
		t.Fatal("FlushCLITokenTouches did not finish")
	}
	if got := readCLITokenLastUsed(t, db, id); !got.Valid {
		t.Fatal("last_used_at not written by the flush")
	}
	if open := db.Stats().OpenConnections; open != 1 {
		t.Fatalf("validation and its touch opened %d connections, want 1", open)
	}
}

func TestCLITokenToucherThrottlesWithinTheInterval(t *testing.T) {
	db := setupTestDB(t)
	id, _ := seedStandardCLIToken(t, db, "throttle")
	w := newCLITokenToucher(time.Hour, time.Hour, time.Hour, 16)
	w.touch(db, id, "first")
	flushToucher(t, w)
	if got := readCLITokenLastUsed(t, db, id); got.String != "first" {
		t.Fatalf("last_used_at = %+v, want first", got)
	}
	w.touch(db, id, "second")
	w.mu.Lock()
	queued := len(w.queues)
	w.mu.Unlock()
	if queued != 0 {
		t.Fatal("a use within the interval queued another write")
	}
	flushToucher(t, w)
	if got := readCLITokenLastUsed(t, db, id); got.String != "first" {
		t.Fatalf("throttled use rewrote last_used_at: %+v", got)
	}
}

func TestCLITokenToucherWritesAgainAfterTheIntervalExpires(t *testing.T) {
	db := setupTestDB(t)
	id, _ := seedStandardCLIToken(t, db, "expiry")
	w := newCLITokenToucher(50*time.Millisecond, time.Hour, time.Hour, 16)
	w.touch(db, id, "first")
	flushToucher(t, w)
	time.Sleep(60 * time.Millisecond)
	w.touch(db, id, "second")
	flushToucher(t, w)
	if got := readCLITokenLastUsed(t, db, id); got.String != "second" {
		t.Fatalf("last_used_at = %+v after the interval expired, want second", got)
	}
}

// The delay elapsing (not only a flush) writes the queue, so last_used_at is
// updated eventually without shutdown.
func TestCLITokenToucherWritesAfterTheDelay(t *testing.T) {
	db := setupTestDB(t)
	id, _ := seedStandardCLIToken(t, db, "delay")
	w := newCLITokenToucher(time.Hour, time.Hour, 10*time.Millisecond, 16)
	w.touch(db, id, "later")
	waitCLITokenLastUsed(t, db, id, func(v sql.NullString) bool { return v.String == "later" })
	flushToucher(t, w)
}

func TestCLITokenToucherCoalescesConcurrentTouches(t *testing.T) {
	db := setupTestDB(t)
	id, _ := seedStandardCLIToken(t, db, "concurrent")
	other, _ := seedStandardCLIToken(t, db, "concurrent-other")
	w := newCLITokenToucher(time.Hour, time.Hour, time.Hour, 16)
	start := make(chan struct{})
	finished := make(chan struct{})
	for i := 0; i < 64; i++ {
		tok := id
		if i%2 == 1 {
			tok = other
		}
		go func() {
			<-start
			w.touch(db, tok, "now")
			finished <- struct{}{}
		}()
	}
	close(start)
	for i := 0; i < 64; i++ {
		<-finished
	}
	w.mu.Lock()
	queues, pending := len(w.queues), w.pending
	w.mu.Unlock()
	if queues != 1 || pending != 2 {
		t.Fatalf("64 concurrent touches of 2 tokens: %d workers, %d pending, want 1 and 2", queues, pending)
	}
	flushToucher(t, w)
	w.mu.Lock()
	written := w.written
	w.mu.Unlock()
	if written != 1 {
		t.Fatalf("%d batch writes, want 1", written)
	}
	for _, tok := range []string{id, other} {
		if got := readCLITokenLastUsed(t, db, tok); got.String != "now" {
			t.Fatalf("last_used_at of %s = %+v, want now", tok, got)
		}
	}
}

// The pending queue is bounded: a touch that does not fit is dropped without
// being throttled, so the next use of that token queues it.
func TestCLITokenToucherBoundsTheQueue(t *testing.T) {
	db := setupTestDB(t)
	a, _ := seedStandardCLIToken(t, db, "bound-a")
	b, _ := seedStandardCLIToken(t, db, "bound-b")
	c, _ := seedStandardCLIToken(t, db, "bound-c")
	w := newCLITokenToucher(time.Hour, time.Hour, time.Hour, 2)
	w.touch(db, a, "now")
	w.touch(db, b, "now")
	w.touch(db, c, "now")
	w.mu.Lock()
	pending := w.pending
	_, throttled := w.next[c]
	w.mu.Unlock()
	if pending != 2 || throttled {
		t.Fatalf("full queue: %d pending, overflow throttled=%v; want 2 and false", pending, throttled)
	}
	flushToucher(t, w)
	if got := readCLITokenLastUsed(t, db, c); got.Valid {
		t.Fatalf("overflowed touch was written: %+v", got)
	}
	w.touch(db, c, "retry")
	flushToucher(t, w)
	if got := readCLITokenLastUsed(t, db, c); got.String != "retry" {
		t.Fatalf("next use after an overflow: last_used_at = %+v, want retry", got)
	}
}

func TestCLITokenToucherEvictsTheThrottleMap(t *testing.T) {
	db := setupTestDB(t)
	id, _ := seedStandardCLIToken(t, db, "evict")
	w := newCLITokenToucher(time.Hour, time.Hour, time.Hour, 4)

	// Expired entries are evicted, recent ones kept.
	w.mu.Lock()
	w.next["expired-1"] = time.Now().Add(-time.Second)
	w.next["expired-2"] = time.Now().Add(-time.Second)
	w.next["expired-3"] = time.Now().Add(-time.Second)
	w.next["recent"] = time.Now().Add(time.Hour)
	w.mu.Unlock()
	w.touch(db, id, "now")
	w.mu.Lock()
	_, recent := w.next["recent"]
	_, touched := w.next[id]
	size := len(w.next)
	w.mu.Unlock()
	if size != 2 || !recent || !touched {
		t.Fatalf("after evicting expired entries: size %d, recent kept=%v, new kept=%v; want 2, true, true", size, recent, touched)
	}
	flushToucher(t, w)

	// When every entry is recent the map is cleared.
	other, _ := seedStandardCLIToken(t, db, "evict-other")
	w.mu.Lock()
	for _, k := range []string{"r1", "r2", "r3"} {
		w.next[k] = time.Now().Add(time.Hour)
	}
	w.mu.Unlock()
	w.touch(db, other, "now")
	w.mu.Lock()
	_, touched = w.next[other]
	size = len(w.next)
	w.mu.Unlock()
	if size != 1 || !touched {
		t.Fatalf("after clearing a full map of recent entries: size %d, new kept=%v; want 1, true", size, touched)
	}
	flushToucher(t, w)
	if got := readCLITokenLastUsed(t, db, other); got.String != "now" {
		t.Fatalf("touch after clearing the map not written: %+v", got)
	}
}

// A failed write must not hide the breadcrumb for a whole interval: the token
// may be queued again after the retry delay, and that write lands.
func TestCLITokenToucherRetriesAFailedWrite(t *testing.T) {
	db := setupTestDB(t)
	id, _ := seedStandardCLIToken(t, db, "retry")
	closed := setupTestDB(t)
	_ = closed.Close()
	w := newCLITokenToucher(time.Hour, 50*time.Millisecond, time.Hour, 16)
	w.touch(closed, id, "lost")
	flushToucher(t, w)
	w.mu.Lock()
	next := w.next[id]
	w.mu.Unlock()
	if wait := time.Until(next); wait > 50*time.Millisecond {
		t.Fatalf("failed touch retries in %v, want within 50ms", wait)
	}
	time.Sleep(60 * time.Millisecond)
	w.touch(db, id, "retried")
	flushToucher(t, w)
	if got := readCLITokenLastUsed(t, db, id); got.String != "retried" {
		t.Fatalf("retry after a failed write: last_used_at = %+v, want retried", got)
	}
}
