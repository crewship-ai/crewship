package work

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// An empty poll has no mutation to serialize with the held writer. The timeout
// is a deadlock guard, not an assertion about a particular number of milliseconds.
func TestClaimIdleQueueDoesNotWaitForWriter(t *testing.T) {
	s, db, _ := newTestStore(t)
	held, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	if _, err := held.ExecContext(t.Context(), `UPDATE work_items SET updated_at=updated_at WHERE 0`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type result struct {
		claimed *Claimed
		err     error
	}
	done := make(chan result, 1)
	go func() { c, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "idle-reader"}); done <- result{c, err} }()
	var got result
	returnedWithWriterHeld := false
	select {
	case got = <-done:
		returnedWithWriterHeld = true
	case <-ctx.Done():
	}
	// Always release before joining a driver busy wait, including on baseline.
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !returnedWithWriterHeld {
		got = <-done
		t.Fatalf("empty claim needed the writer to release its transaction: %v", got.err)
	}
	if got.claimed != nil || !errors.Is(got.err, ErrNoWork) {
		t.Fatalf("got %#v, %v; want no work", got.claimed, got.err)
	}
}

func TestClaimIdlePrecheckPreservesGlobalExpiry(t *testing.T) {
	s, db, clock := newTestStore(t)
	var ids []string
	for _, state := range []State{StateQueued, StateRetryWait} {
		req := backgroundReq("expired-agent")
		req.EligibleAt = clock.Now().Add(time.Hour)
		req.DeadlineAt = clock.Now().Add(-time.Second)
		r := accept(t, s, db, req)
		if state == StateRetryWait {
			if _, err := db.Exec(`UPDATE work_items SET state='retry_wait' WHERE id=?`, r.WorkID); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, r.WorkID)
	}
	// None of these options matches the overdue rows. Maintenance is global.
	got, err := s.Claim(t.Context(), ClaimOptions{LeaseOwner: "other-worker", Class: ClassChat,
		AgentID: "different-agent", WorkID: "missing-work", Kinds: []Kind{{Source: SourceChat, DomainKind: "different-kind"}}})
	if got != nil || !errors.Is(err, ErrNoWork) {
		t.Fatalf("got %#v, %v; want no work", got, err)
	}
	for _, id := range ids {
		item, err := s.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if item.State != StateExpired || item.StateReason != "deadline passed before start" {
			t.Fatalf("maintenance lost: %#v", item)
		}
		events, err := s.History(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 2 || events[1].ToState != StateExpired {
			t.Fatalf("expiry event missing: %#v", events)
		}
	}
	if got := countRows(t, db, "work_attempts"); got != 0 {
		t.Fatalf("maintenance started %d attempts", got)
	}
}

func TestClaimIdlePrecheckDoesNotCacheEmptyQueue(t *testing.T) {
	s, db, _ := newTestStore(t)
	opts := ClaimOptions{LeaseOwner: "worker"}
	if got, err := s.Claim(t.Context(), opts); got != nil || !errors.Is(err, ErrNoWork) {
		t.Fatalf("initial: %#v %v", got, err)
	}
	r := accept(t, s, db, backgroundReq("new-agent"))
	got, err := s.Claim(t.Context(), opts)
	if err != nil || got == nil || got.Item.ID != r.WorkID {
		t.Fatalf("new acceptance hidden: %#v %v", got, err)
	}
}

func TestClaimIdlePrecheckPropagatesContextError(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "worker"})
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("read error hidden as no work: %#v %v", got, err)
	}
}

type idleClaimSnapshot struct {
	Claimed   *Claimed
	NoWork    bool
	Items     []*Item
	Histories [][]Event
	Attempts  int
}

func TestClaimIdlePrecheckTransactionalParity(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*testing.T, *Store, *sql.DB, *fixedClock) []string
		opts    ClaimOptions
	}{
		{name: "empty"},
		{name: "future", prepare: func(t *testing.T, s *Store, db *sql.DB, c *fixedClock) []string {
			req := backgroundReq("future-agent")
			req.EligibleAt = c.Now().Add(time.Hour)
			return []string{accept(t, s, db, req).WorkID}
		}},
		{name: "runnable", prepare: func(t *testing.T, s *Store, db *sql.DB, c *fixedClock) []string {
			return []string{accept(t, s, db, backgroundReq("runnable-agent")).WorkID}
		}},
		{name: "overdue-only", prepare: func(t *testing.T, s *Store, db *sql.DB, c *fixedClock) []string {
			req := backgroundReq("expired-agent")
			req.EligibleAt = c.Now().Add(time.Hour)
			req.DeadlineAt = c.Now().Add(-time.Second)
			return []string{accept(t, s, db, req).WorkID}
		}},
		{name: "options-exclude-runnable", opts: ClaimOptions{AgentID: "other-agent", Class: ClassChat, WorkID: "missing"}, prepare: func(t *testing.T, s *Store, db *sql.DB, c *fixedClock) []string {
			return []string{accept(t, s, db, backgroundReq("queued-agent")).WorkID}
		}},
		{name: "exhausted-before-runnable", prepare: func(t *testing.T, s *Store, db *sql.DB, c *fixedClock) []string {
			exhausted := accept(t, s, db, backgroundReq("exhausted-agent")).WorkID
			if _, err := db.Exec(`UPDATE work_items SET attempts=? WHERE id=?`, MaxAttempts, exhausted); err != nil {
				t.Fatal(err)
			}
			c.Advance(time.Second)
			next := accept(t, s, db, backgroundReq("runnable-agent")).WorkID
			return []string{exhausted, next}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := func(reference bool) idleClaimSnapshot {
				s, db, c := newTestStore(t)
				var ids []string
				if tc.prepare != nil {
					ids = tc.prepare(t, s, db, c)
				}
				opts := tc.opts
				opts.LeaseOwner = "worker"
				var claimed *Claimed
				var err error
				if reference {
					claimed, err = s.referenceClaimBeforeIdlePrecheck(t.Context(), opts)
				} else {
					claimed, err = s.Claim(t.Context(), opts)
				}
				if err != nil && !errors.Is(err, ErrNoWork) {
					t.Fatalf("claim reference=%v: %v", reference, err)
				}
				out := idleClaimSnapshot{Claimed: claimed, NoWork: errors.Is(err, ErrNoWork), Attempts: countRows(t, db, "work_attempts")}
				for _, id := range ids {
					item, err := s.Get(t.Context(), id)
					if err != nil {
						t.Fatal(err)
					}
					events, err := s.History(t.Context(), id)
					if err != nil {
						t.Fatal(err)
					}
					out.Items = append(out.Items, item)
					out.Histories = append(out.Histories, events)
				}
				return out
			}
			want, got := snapshot(true), snapshot(false)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("transactional behavior changed:\ngot %#v\nwant %#v", got, want)
			}
		})
	}
}

// The read hint must not freeze the clock used after acquiring the writer.
func TestClaimIdlePrecheckResamplesTransactionTime(t *testing.T) {
	s, db, clock := newTestStore(t)
	req := backgroundReq("deadline-agent")
	req.DeadlineAt = clock.Now().Add(time.Second)
	id := accept(t, s, db, req).WorkID
	held, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	hinted := make(chan struct{})
	var first sync.Once
	s = s.WithClock(func() time.Time {
		now := clock.Now()
		first.Do(func() { close(hinted) })
		return now
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		got, err := s.Claim(ctx, ClaimOptions{LeaseOwner: "deadline-worker"})
		if got != nil {
			err = errors.New("started work after its deadline")
		}
		done <- err
	}()
	select {
	case <-hinted:
	case <-ctx.Done():
		held.Rollback()
		<-done
		t.Fatal("claim did not reach its read hint")
	}
	clock.Advance(2 * time.Second)
	rollbackErr := held.Rollback()
	err = <-done
	if rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim: %v", err)
	}
	item, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != StateExpired {
		t.Fatalf("old hint time reused: %#v", item)
	}
	if got := countRows(t, db, "work_attempts"); got != 0 {
		t.Fatalf("started %d attempts", got)
	}
}
