package documents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func acquire(t *testing.T, s *Store) Lease {
	t.Helper()
	l, e := s.Acquire(ctx, key, "node-instance", allow)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func commit(l Lease, id string, rev int64) Commit {
	return Commit{Lease: l, ID: id, Fingerprint: "semantic", IfRev: rev, Schema: 1, Update: []byte("incremental"), CandidateSnapshot: []byte("candidate"), Projection: projection, IfHashes: map[string]string{"one": "h1"}, Kind: "edit"}
}
func TestRestartReplayReceiptsAndWriterFence(t *testing.T) {
	s, db, p := fixture(t, Options{})
	l := acquire(t, s)
	c := commit(l, "request", 0)
	r, e := s.Commit(ctx, key, c, allow)
	if e != nil {
		t.Fatal(e)
	}
	if r.Actor != "server-actor" {
		t.Fatal(r)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db2 := openTest(t, p)
	defer db2.Close()
	s = New(db2.DB, Options{})
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 1 || string(v.Snapshot) != "baseline" || len(v.Updates) != 1 {
		t.Fatalf("restart state %#v %v", v, e)
	}
	if e = s.InvalidateLeases(ctx); e != nil {
		t.Fatal(e)
	}
	r2, found, e := s.LookupReceipt(ctx, key, c.ID, c.Fingerprint, allow)
	if e != nil || !found || r2 != r {
		t.Fatalf("receipt replay %v %v", r2, e)
	}
	newLease := acquire(t, s)
	c.Lease = newLease
	c.Update = []byte("changed")
	if _, e = s.Commit(ctx, key, c, allow); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("payload reuse: %v", e)
	}
	c = commit(l, "late", 1)
	if _, e = s.Commit(ctx, key, c, allow); !errors.Is(e, ErrFence) {
		t.Fatalf("old process: %v", e)
	}
	denied := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("revoked") }
	if _, e = s.Commit(ctx, key, commit(l, "request", 0), denied); e == nil || e.Error() != "revoked" {
		t.Fatalf("receipt bypassed authorization: %v", e)
	}
}
func TestConcurrentCASAndWorkspaceIsolation(t *testing.T) {
	s, _, _ := fixture(t, Options{})
	l := acquire(t, s)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.Commit(ctx, key, commit(l, fmt.Sprint(i), 0), allow)
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	other := Key{"workspace-b", "page-b"}
	if _, e := s.Read(ctx, other, allow); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e := s.Init(ctx, other, 1, []byte("other baseline"), projection, allow); e != nil {
		t.Fatal(e)
	}
	v, e := s.Read(ctx, other, allow)
	if e != nil || v.HeadRev != 0 || len(v.Updates) != 0 {
		t.Fatalf("isolation %#v %v", v, e)
	}
	if _, e = s.Acquire(ctx, key, "another-process", allow); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
}
func TestLeaseExpiryAndUnknownSchema(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	old := acquire(t, s)
	if _, e := db.Exec("UPDATE documents SET lease_until=0 WHERE workspace_id=? AND page_id=?", key.WorkspaceID, key.PageID); e != nil {
		t.Fatal(e)
	}
	current := acquire(t, s)
	if current.Writer == old.Writer || current.Epoch <= old.Epoch {
		t.Fatal("lease identity reused")
	}
	if _, e := s.Commit(ctx, key, commit(old, "expired", 0), allow); !errors.Is(e, ErrFence) {
		t.Fatal(e)
	}
	if _, e := db.Exec("UPDATE documents SET schema_version=2,writer='',lease_until=0 WHERE workspace_id=? AND page_id=?", key.WorkspaceID, key.PageID); e != nil {
		t.Fatal(e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.Schema != 2 || string(v.Snapshot) != "baseline" {
		t.Fatal(v, e)
	}
	if _, e := s.Acquire(ctx, key, "future-reader", allow); !errors.Is(e, ErrSchema) {
		t.Fatal(e)
	}
}
func TestLogQuotaDoesNotLoseHistory(t *testing.T) {
	s, _, _ := fixture(t, Options{MaxLogBytes: 15})
	l := acquire(t, s)
	if _, e := s.Commit(ctx, key, commit(l, "one", 0), allow); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Commit(ctx, key, commit(l, "two", 1), allow); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	v, e := s.Read(ctx, key, allow)
	if e != nil || v.HeadRev != 1 || len(v.Updates) != 1 || string(v.Snapshot) != "baseline" {
		t.Fatal(v, e)
	}
}

func TestCommitOriginalPreconditionsAndGuard(t *testing.T) {
	denied := errors.New("target no longer authorized")
	for _, tt := range []struct {
		name       string
		rev        int64
		generation int64
		hash       string
		guard      MutationGuard
		want       error
	}{
		{"current target", 0, 0, "h1", nil, nil},
		{"stale revision", 1, 0, "h1", nil, ErrConflict},
		{"stale generation", 0, 1, "h1", nil, ErrConflict},
		{"stale hash", 0, 0, "changed", nil, ErrConflict},
		{"guard rejects target", 0, 0, "h1", func(context.Context, *sql.Tx, string, State) error { return denied }, denied},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, db, _ := fixture(t, Options{})
			c := commit(acquire(t, s), "request", tt.rev)
			c.IfGeneration = &tt.generation
			c.IfHashes["one"] = tt.hash
			c.Guard = tt.guard
			receipt, err := s.Commit(ctx, key, c, allow)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
			var count int
			if err := db.QueryRow("SELECT count(*) FROM document_receipts WHERE id='request'").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if tt.want != nil {
				state, err := s.Read(ctx, key, allow)
				if err != nil || state.HeadRev != 0 || len(state.Updates) != 0 || count != 0 {
					t.Fatalf("rejected mutation persisted: %#v count=%d err=%v", state, count, err)
				}
			} else if count != 1 || receipt.Rev != 1 || receipt.Actor != "server-actor" {
				t.Fatalf("commit receipt: %#v count=%d", receipt, count)
			}
		})
	}
}

func TestCommitStorageFailureRollsBackUpdateAndReceipt(t *testing.T) {
	s, db, _ := fixture(t, Options{})
	lease := acquire(t, s)
	if _, err := db.Exec("CREATE TRIGGER fail_document_head BEFORE UPDATE OF head_rev ON documents BEGIN SELECT RAISE(ABORT, 'injected storage failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, key, commit(lease, "rollback", 0), allow); err == nil {
		t.Fatal("expected storage failure")
	}
	state, err := s.Read(ctx, key, allow)
	if err != nil || state.HeadRev != 0 || len(state.Updates) != 0 {
		t.Fatalf("rollback state %#v: %v", state, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM document_receipts WHERE id='rollback'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback receipts%d: %v", count, err)
	}
}

func TestLeaseIdentityChecksAndRelease(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Lease)
	}{
		{"writer", func(l *Lease) { l.Writer = "other-writer" }},
		{"owner", func(l *Lease) { l.Owner = "other-owner" }},
		{"epoch", func(l *Lease) { l.Epoch++ }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := fixture(t, Options{})
			lease := acquire(t, s)
			stale := lease
			tt.mutate(&stale)
			if _, err := s.Renew(ctx, key, stale, allow); !errors.Is(err, ErrFence) {
				t.Fatalf("renew: %v", err)
			}
			if err := s.Release(ctx, key, stale, allow); !errors.Is(err, ErrFence) {
				t.Fatalf("release: %v", err)
			}
			renewed, err := s.Renew(ctx, key, lease, allow)
			if err != nil || renewed.Writer != lease.Writer || renewed.Epoch != lease.Epoch || renewed.Generation != lease.Generation {
				t.Fatalf("current lease renewal %#v: %v", renewed, err)
			}
			if err := s.Release(ctx, key, renewed, allow); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Renew(ctx, key, renewed, allow); !errors.Is(err, ErrFence) {
				t.Fatalf("released lease renewal: %v", err)
			}
			next := acquire(t, s)
			if next.Epoch <= lease.Epoch || next.Writer == lease.Writer {
				t.Fatalf("lease reused: %#v", next)
			}
		})
	}
}

func TestCommitRetryBindsOriginalGenerationAndActor(t *testing.T) {
	for _, tt := range []struct {
		name       string
		generation int64
		actor      string
		revoke     bool
		rotate     bool
		want       error
	}{
		{"exact retry", 0, "server-actor", false, false, nil},
		{"exact retry after boot fencing", 0, "server-actor", false, true, nil},
		{"changed generation", 1, "server-actor", false, false, ErrDuplicate},
		{"changed actor", 0, "other-actor", false, false, ErrDuplicate},
		{"revoked authority", 0, "", true, false, errors.New("revoked")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := fixture(t, Options{})
			c := commit(acquire(t, s), "generation-retry", 0)
			originalGeneration := int64(0)
			c.IfGeneration = &originalGeneration
			original, err := s.Commit(ctx, key, c, allow)
			if err != nil {
				t.Fatal(err)
			}
			if tt.rotate {
				if err := s.InvalidateLeases(ctx); err != nil {
					t.Fatal(err)
				}
				c.Lease = acquire(t, s)
			}
			c.IfGeneration = &tt.generation
			auth := func(context.Context, *sql.Tx) (string, error) {
				if tt.revoke {
					return "", tt.want
				}
				return tt.actor, nil
			}
			got, err := s.Commit(ctx, key, c, auth)
			if !errors.Is(err, tt.want) {
				t.Fatalf("retry got %#v, %v; want %v", got, err, tt.want)
			}
			if err == nil && got != original {
				t.Fatalf("exact retry receipt changed: %#v, want %#v", got, original)
			}
		})
	}
}

func TestRequestDigestRetainsNilGenerationIdentity(t *testing.T) {
	c := commit(Lease{}, "request", 0)
	got, err := RequestDigest(c)
	// Frozen source digest for the original request without a generation condition.
	const original = "a657431b1c64d72dd89f7361b11a8e0cc2a8477f6474b0367970e5eedb78196f"
	if err != nil || got != original {
		t.Fatalf("nil generation digest %q: %v", got, err)
	}
	c.Lease = Lease{Writer: "rotated", Owner: "new-service", Epoch: 999}
	rotated, err := RequestDigest(c)
	if err != nil || rotated != original {
		t.Fatalf("lease changed request digest %q: %v", rotated, err)
	}
}
