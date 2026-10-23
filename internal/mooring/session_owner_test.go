package mooring

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

type ownerTestClock struct {
	mu   sync.Mutex
	tick time.Duration
	boot string
	err  error
}

func (c *ownerTestClock) Now() (string, time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.boot, c.tick, c.err
}
func (c *ownerTestClock) advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.tick += d }
func ownerFixture(t *testing.T) (*OwnerStore, *database.DB, *ownerTestClock, SessionKey) {
	t.Helper()
	db, e := database.Open("file:" + filepath.Join(t.TempDir(), "owners.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`INSERT INTO users(id,email) VALUES('u','owner@example.test')`, `INSERT INTO workspaces(id,name,slug) VALUES('w','W','w')`, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('p','w','p','P','u','{}')`} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	clock := &ownerTestClock{boot: "boot-a", tick: time.Second}
	return NewOwnerStore(db.DB, true, clock, 5*time.Second), db, clock, SessionKey{WorkspaceID: "w", ArtifactID: "p", ApplicationID: "editor"}
}
func checkOwnerTx(t *testing.T, s *OwnerStore, db *sql.DB, l OwnerLease) error {
	t.Helper()
	tx, e := db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	return s.Check(t.Context(), tx, l)
}
func TestOwnerDisabledDoesNotAccessDatabase(t *testing.T) {
	s := NewOwnerStore(nil, false, nil, time.Second)
	if _, e := s.Acquire(t.Context(), SessionKey{}, "runtime"); !errors.Is(e, ErrOwnerDisabled) {
		t.Fatalf("disabled: %v", e)
	}
}
func TestOwnerAcquireConcurrentAndFenceExpiredHolder(t *testing.T) {
	s, db, clock, key := ownerFixture(t)
	var wg sync.WaitGroup
	results := make(chan OwnerLease, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, e := s.Acquire(context.Background(), key, "runtime")
			if e != nil {
				errs <- e
			} else {
				results <- l
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(results) != 1 || len(errs) != 1 {
		t.Fatalf("owners=%d errors=%d", len(results), len(errs))
	}
	first := <-results
	for e := range errs {
		if !errors.Is(e, ErrOwnerBusy) {
			t.Fatal(e)
		}
	}
	if e := checkOwnerTx(t, s, db.DB, first); e != nil {
		t.Fatal(e)
	}
	clock.advance(5 * time.Second)
	if e := checkOwnerTx(t, s, db.DB, first); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("expired owner accepted: %v", e)
	}
	second, e := s.Acquire(t.Context(), key, "replacement")
	if e != nil {
		t.Fatal(e)
	}
	if second.Generation != first.Generation+1 || second.Token == first.Token {
		t.Fatalf("takeover: first=%d second=%d", first.Generation, second.Generation)
	}
	if _, e = s.Renew(t.Context(), first); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("old renew: %v", e)
	}
	if e = checkOwnerTx(t, s, db.DB, first); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("old commit: %v", e)
	}
	if e = checkOwnerTx(t, s, db.DB, second); e != nil {
		t.Fatal(e)
	}
}
func TestOwnerScopeTokenAndBootAreCheckedInWriterTransaction(t *testing.T) {
	s, db, clock, key := ownerFixture(t)
	l, e := s.Acquire(t.Context(), key, "runtime")
	if e != nil {
		t.Fatal(e)
	}
	for _, alter := range []func(*OwnerLease){func(x *OwnerLease) { x.Key.WorkspaceID = "other" }, func(x *OwnerLease) { x.Key.ApplicationID = "other" }, func(x *OwnerLease) { x.Token = "forged" }, func(x *OwnerLease) { x.Generation++ }, func(x *OwnerLease) { x.OwnerID = "other" }} {
		bad := l
		alter(&bad)
		if e = checkOwnerTx(t, s, db.DB, bad); !errors.Is(e, ErrOwnerStale) {
			t.Fatalf("scope/token accepted: %v", e)
		}
	}
	clock.mu.Lock()
	clock.boot = "boot-b"
	clock.tick = time.Millisecond
	clock.mu.Unlock()
	if e = checkOwnerTx(t, s, db.DB, l); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("previous boot accepted: %v", e)
	}
	next, e := s.Acquire(t.Context(), key, "new-boot-runtime")
	if e != nil {
		t.Fatal(e)
	}
	if next.Generation != l.Generation+1 {
		t.Fatal("boot takeover did not advance generation")
	}
}
func TestOwnerRenewRequiresUnexpiredLeaseAndRetainsGeneration(t *testing.T) {
	s, db, clock, key := ownerFixture(t)
	first, e := s.Acquire(t.Context(), key, "runtime")
	if e != nil {
		t.Fatal(e)
	}
	clock.advance(time.Second)
	renewed, e := s.Renew(t.Context(), first)
	if e != nil {
		t.Fatal(e)
	}
	if renewed.Generation != first.Generation || renewed.Token != first.Token {
		t.Fatal("renew changed owner")
	}
	clock.advance(4 * time.Second)
	if e = checkOwnerTx(t, s, db.DB, renewed); e != nil {
		t.Fatal(e)
	}
	clock.advance(time.Second)
	if _, e = s.Renew(t.Context(), renewed); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("expiry resurrected: %v", e)
	}
}
func TestOwnerClockFailureAndUnknownArtifactFailClosed(t *testing.T) {
	s, _, clock, key := ownerFixture(t)
	clock.err = errors.New("clock unavailable")
	if _, e := s.Acquire(t.Context(), key, "runtime"); e == nil {
		t.Fatal("missing clock accepted")
	}
	clock.err = nil
	key.WorkspaceID = "other"
	if _, e := s.Acquire(t.Context(), key, "runtime"); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("wrong workspace: %v", e)
	}
}

func TestOwnerRecoveryUsesPersistedGenerationAfterDatabaseReopen(t *testing.T) {
	s, db, _, key := ownerFixture(t)
	first, e := s.Acquire(t.Context(), key, "first-process")
	if e != nil {
		t.Fatal(e)
	}
	var seq int
	var name, path string
	if e = db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := database.Open("file:" + path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { reopened.Close() })
	// New storage object and clock: no state from the original owner store is used.
	recovered := NewOwnerStore(reopened.DB, true, &ownerTestClock{boot: "boot-a", tick: 7 * time.Second}, 5*time.Second)
	replacement, e := recovered.Acquire(t.Context(), key, "replacement-process")
	if e != nil {
		t.Fatal(e)
	}
	if replacement.Generation != first.Generation+1 {
		t.Fatalf("persisted generation lost: %d -> %d", first.Generation, replacement.Generation)
	}
	if e = checkOwnerTx(t, recovered, reopened.DB, first); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("old process accepted after recovery: %v", e)
	}
}
func TestOwnerDeletedArtifactCannotRenewOrCommit(t *testing.T) {
	s, db, _, key := ownerFixture(t)
	lease, e := s.Acquire(t.Context(), key, "runtime")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`DELETE FROM pages WHERE id='p'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Renew(t.Context(), lease); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("deleted artifact renew: %v", e)
	}
	if e = checkOwnerTx(t, s, db.DB, lease); !errors.Is(e, ErrOwnerStale) {
		t.Fatalf("deleted artifact commit: %v", e)
	}
}

func TestOwnerAcquireRejectsWeakerDurability(t *testing.T) {
	s, db, _, key := ownerFixture(t)
	db.SetMaxOpenConns(1)
	if _, e := db.Exec(`PRAGMA synchronous=NORMAL`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Acquire(t.Context(), key, "runtime"); !errors.Is(e, ErrInvalid) {
		t.Fatalf("non-durable owner acknowledged: %v", e)
	}
}
