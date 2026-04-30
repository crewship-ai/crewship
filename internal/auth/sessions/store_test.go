package sessions

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	_ "modernc.org/sqlite"
)

// newTestDB returns a fully migrated SQLite DB backed by a file in
// t.TempDir(). The previous in-memory shape (`:memory:`) gave each
// pool connection a private schema, which broke any test that ran
// goroutines against the same store — reads on the second connection
// found "no such table". File-backed survives connection rotation
// without requiring `cache=shared` query-string gymnastics.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+dir+"/sessions.db?_foreign_keys=on&_journal=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Single-writer SQLite + WAL mode + busy-timeout is the safe combo
	// for parallel-goroutine tests. Without these, two goroutines
	// trying to UPDATE the same row simultaneously race for the file
	// lock and the loser sees SQLITE_BUSY before the timeout kicks in.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Sessions reference users(id); insert a synthetic row.
	_, err = db.Exec(`INSERT INTO users (id, email, full_name, hashed_password) VALUES (?, ?, ?, ?)`,
		"u1", "u1@example.com", "User One", "$2a$10$xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return db
}

func TestCreateAndGet(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()

	sess, err := store.Create(ctx, "u1", "Mozilla/5.0", "1.2.3.4", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.ID == "" || sess.UserID != "u1" {
		t.Fatalf("unexpected session: %+v", sess)
	}

	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != sess.ID {
		t.Errorf("id mismatch: %s vs %s", got.ID, sess.ID)
	}
	if got.RevokedAt != nil {
		t.Errorf("expected active, got revoked at %v", got.RevokedAt)
	}
	if !got.Active(time.Now()) {
		t.Error("expected Active=true")
	}
}

func TestGetNotFound(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	_, err := store.Get(context.Background(), "s_nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRevoke(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Revoke(ctx, sess.ID, ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get after revoke: %v", err)
	}
	if got.RevokedAt == nil {
		t.Fatal("RevokedAt should be set")
	}
	if got.RevokedReason != ReasonLogout {
		t.Errorf("reason: got %q want %q", got.RevokedReason, ReasonLogout)
	}
	if got.Active(time.Now()) {
		t.Error("revoked session should not be Active")
	}
}

func TestRevokeIdempotent(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Revoke(ctx, sess.ID, ReasonLogout); err != nil {
		t.Fatalf("first revoke: %v", err)
	}
	if err := store.Revoke(ctx, sess.ID, ReasonAdminForce); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RevokedReason != ReasonAdminForce {
		t.Errorf("expected reason to update on second revoke, got %q", got.RevokedReason)
	}
}

func TestRevokeUnknownReturnsNotFound(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	err := store.Revoke(context.Background(), "s_nope", ReasonLogout)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestExpiredSessionNotActive(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()

	// Set the clock back so the row's expires_at is in the past.
	past := time.Now().Add(-2 * time.Hour)
	store.SetClock(func() time.Time { return past })
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Restore real clock; the session should now be expired.
	store.SetClock(time.Now)
	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Active(time.Now()) {
		t.Error("expired session should not be Active")
	}
}

func TestListActiveForUser(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()

	a, err := store.Create(ctx, "u1", "iOS", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	b, err := store.Create(ctx, "u1", "Android", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	c, err := store.Create(ctx, "u1", "Web", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Revoke b so only a + c are active.
	if err := store.Revoke(ctx, b.ID, ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	list, err := store.ListActiveForUser(ctx, "u1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 active, got %d", len(list))
	}
	ids := map[string]bool{}
	for _, s := range list {
		ids[s.ID] = true
	}
	if !ids[a.ID] || !ids[c.ID] {
		t.Errorf("expected active ids %s,%s; got %v", a.ID, c.ID, ids)
	}
	if ids[b.ID] {
		t.Error("revoked session leaked into ListActiveForUser")
	}
}

func TestRevokeAllForUser(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.Create(ctx, "u1", "", "", time.Hour); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	n, err := store.RevokeAllForUser(ctx, "u1", ReasonPasswordChange)
	if err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if n != 3 {
		t.Errorf("revoked %d, want 3", n)
	}
	list, err := store.ListActiveForUser(ctx, "u1")
	if err != nil {
		t.Fatalf("ListActiveForUser: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 active after revoke-all, got %d", len(list))
	}
}

func TestTouchLastUsedThrottled(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First touch goes through.
	first := time.Now().Add(time.Hour) // pretend "now" is 1h after create
	store.SetClock(func() time.Time { return first })
	if err := store.TouchLastUsed(ctx, sess.ID); err != nil {
		t.Fatalf("first touch: %v", err)
	}

	// Second touch within the throttle window must NOT write.
	store.SetClock(func() time.Time { return first.Add(10 * time.Second) })
	if err := store.TouchLastUsed(ctx, sess.ID); err != nil {
		t.Fatalf("second touch: %v", err)
	}

	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.LastUsedAt.Equal(first.UTC().Truncate(time.Second)) {
		t.Errorf("LastUsedAt: got %v, want %v (second touch should have been throttled)",
			got.LastUsedAt, first)
	}

	// Third touch past the throttle window writes.
	third := first.Add(LastUsedThrottle + 5*time.Second)
	store.SetClock(func() time.Time { return third })
	if err := store.TouchLastUsed(ctx, sess.ID); err != nil {
		t.Fatalf("third touch: %v", err)
	}

	got, err = store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get after third touch: %v", err)
	}
	if !got.LastUsedAt.Equal(third.UTC().Truncate(time.Second)) {
		t.Errorf("LastUsedAt: got %v, want %v (third touch should have written)",
			got.LastUsedAt, third)
	}
}

func TestTouchLastUsedSkipsRevoked(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	sess, err := store.Create(ctx, "u1", "", "", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	originalLastUsed, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := store.Revoke(ctx, sess.ID, ReasonLogout); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Touching a revoked session must not bump last_used_at — the
	// row is gone from the user's perspective and stale touches
	// would smear the audit trail.
	if err := store.TouchLastUsed(ctx, sess.ID); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, err := store.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.LastUsedAt.Equal(originalLastUsed.LastUsedAt) {
		t.Errorf("LastUsedAt advanced on revoked session: %v -> %v",
			originalLastUsed.LastUsedAt, got.LastUsedAt)
	}
}

func TestCreateInvalidArgs(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	if _, err := store.Create(ctx, "", "", "", time.Hour); err == nil {
		t.Error("expected error for empty user id")
	}
	if _, err := store.Create(ctx, "u1", "", "", 0); err == nil {
		t.Error("expected error for zero ttl")
	}
	if _, err := store.Create(ctx, "u1", "", "", -time.Hour); err == nil {
		t.Error("expected error for negative ttl")
	}
}

func TestUserAgentTrimmed(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	ctx := context.Background()
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'A'
	}
	sess, err := store.Create(ctx, "u1", string(long), "", time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(sess.UserAgent) != 250 {
		t.Errorf("UA not trimmed: got len %d, want 250", len(sess.UserAgent))
	}
}
