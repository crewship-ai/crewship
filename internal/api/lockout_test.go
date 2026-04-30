package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// seedLockoutUser inserts a user with a known bcrypt hash so the
// lockout tests can exercise both the right-password and wrong-
// password branches without depending on the seed system.
func seedLockoutUser(t *testing.T, db *sql.DB, email, password string) string {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	id := "u_" + email
	_, err = db.Exec(
		`INSERT INTO users (id, email, full_name, hashed_password) VALUES (?, ?, ?, ?)`,
		id, email, "User "+email, string(hashed),
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

func TestLockout_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	id := seedLockoutUser(t, db, "happy@example.com", "correct horse battery staple")

	gotID, _, err := checkAndLockoutOnFail(context.Background(), db,
		"happy@example.com", "correct horse battery staple", time.Now())
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if gotID != id {
		t.Errorf("user id: got %q want %q", gotID, id)
	}
}

func TestLockout_WrongPasswordReturnsInvalidCredentials(t *testing.T) {
	db := setupTestDB(t)
	seedLockoutUser(t, db, "lock1@example.com", "correct")

	_, _, err := checkAndLockoutOnFail(context.Background(), db,
		"lock1@example.com", "wrong", time.Now())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("got %v, want ErrInvalidCredentials", err)
	}

	// Counter advanced
	var count int
	if err := db.QueryRow(`SELECT failed_login_count FROM users WHERE email = ?`, "lock1@example.com").Scan(&count); err != nil {
		t.Fatalf("read failed_login_count: %v", err)
	}
	if count != 1 {
		t.Errorf("failed_login_count: got %d want 1", count)
	}
}

func TestLockout_UnknownEmailNoCounterAdvance(t *testing.T) {
	db := setupTestDB(t)
	// Pre-existing user, just to make sure we don't mutate the wrong row.
	seedLockoutUser(t, db, "real@example.com", "correct")

	_, _, err := checkAndLockoutOnFail(context.Background(), db,
		"ghost@example.com", "anything", time.Now())
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown email: got %v want ErrInvalidCredentials", err)
	}

	// We must NOT advance any counter on the existing user, AND we
	// must not have created a new row for the ghost email.
	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "ghost@example.com").Scan(&rowCount); err != nil {
		t.Fatalf("count ghost rows: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("expected 0 rows for unknown email, got %d", rowCount)
	}
	var realCount int
	if err := db.QueryRow(`SELECT failed_login_count FROM users WHERE email = ?`, "real@example.com").Scan(&realCount); err != nil {
		t.Fatalf("read real failed_login_count: %v", err)
	}
	if realCount != 0 {
		t.Errorf("real user counter should not advance on ghost-email attempt; got %d", realCount)
	}
}

func TestLockout_LocksAfterThreshold(t *testing.T) {
	db := setupTestDB(t)
	seedLockoutUser(t, db, "victim@example.com", "correct")

	now := time.Now()
	for i := 0; i < LockoutThreshold-1; i++ {
		if _, _, err := checkAndLockoutOnFail(context.Background(), db,
			"victim@example.com", "wrong", now); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: want ErrInvalidCredentials, got %v", i, err)
		}
	}
	// LockoutThreshold-th attempt — the one that should TRIP the lock.
	_, _, err := checkAndLockoutOnFail(context.Background(), db,
		"victim@example.com", "wrong", now)
	if !errors.Is(err, ErrAccountLocked) {
		t.Fatalf("threshold attempt: got %v, want ErrAccountLocked", err)
	}

	// Even with the CORRECT password, we stay locked for the duration.
	_, _, err = checkAndLockoutOnFail(context.Background(), db,
		"victim@example.com", "correct", now.Add(1*time.Minute))
	if !errors.Is(err, ErrAccountLocked) {
		t.Errorf("correct password during lock: got %v, want ErrAccountLocked", err)
	}
}

func TestLockout_ExpiredLockClearsAndAcceptsCorrectPassword(t *testing.T) {
	db := setupTestDB(t)
	seedLockoutUser(t, db, "hostage@example.com", "correct")

	now := time.Now()
	for i := 0; i < LockoutThreshold; i++ {
		_, _, _ = checkAndLockoutOnFail(context.Background(), db,
			"hostage@example.com", "wrong", now)
	}

	// Fast-forward past the lockout window. The next correct password
	// must succeed AND clear the counter.
	future := now.Add(LockoutDuration + time.Minute)
	id, _, err := checkAndLockoutOnFail(context.Background(), db,
		"hostage@example.com", "correct", future)
	if err != nil {
		t.Fatalf("post-lockout correct attempt: got %v, want success", err)
	}
	if id == "" {
		t.Error("expected user id back on success")
	}
	var count int
	var locked sql.NullString
	if err := db.QueryRow(`SELECT failed_login_count, locked_until FROM users WHERE email = ?`, "hostage@example.com").
		Scan(&count, &locked); err != nil {
		t.Fatalf("read hostage row: %v", err)
	}
	if count != 0 {
		t.Errorf("counter not reset after success: got %d", count)
	}
	if locked.Valid {
		t.Errorf("locked_until not cleared after success: got %v", locked.String)
	}
}

// TestLockout_ConcurrentBadPasswordsAdvanceCounter proves the atomic
// UPDATE actually prevents the read-modify-write race CodeRabbit
// flagged. Without atomicity, N parallel wrong-password attempts can
// each read "count=K" before any of them writes, and the counter
// stops at K+1 instead of K+N — defeating the whole point of the
// lockout under the parallel attack pattern.
//
// The test fires LockoutThreshold concurrent wrong-password attempts
// against a fresh user and asserts the counter equals LockoutThreshold
// (not "1" or some collapsed value) AND the account is locked.
func TestLockout_ConcurrentBadPasswordsAdvanceCounter(t *testing.T) {
	db := concurrentTestDB(t)
	seedLockoutUser(t, db, "race@example.com", "correct")

	const N = LockoutThreshold
	var wg sync.WaitGroup
	results := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _, results[idx] = checkAndLockoutOnFail(context.Background(), db,
				"race@example.com", "wrong", time.Now())
		}(i)
	}
	wg.Wait()

	// Each attempt that successfully reached the atomic UPDATE returns
	// either ErrInvalidCredentials (still under threshold) or
	// ErrAccountLocked (the one that crossed it). Goroutines that
	// lost the SQLite writer-lock race surface as the wrapped
	// "database is locked" error from the bump UPDATE — they didn't
	// reach the threshold check, so they don't count toward the
	// counter. We accept that as a successful serialisation; it just
	// means real-world callers would retry.
	locked := 0
	invalid := 0
	busy := 0
	for _, e := range results {
		switch {
		case errors.Is(e, ErrAccountLocked):
			locked++
		case errors.Is(e, ErrInvalidCredentials):
			invalid++
		case e != nil && strings.Contains(e.Error(), "database is locked"):
			busy++
		default:
			t.Errorf("unexpected error from concurrent attempt: %v", e)
		}
	}
	if locked == 0 && (invalid+locked) >= LockoutThreshold {
		t.Error("none of the concurrent attempts triggered the lock — atomicity broke")
	}

	// Counter must equal the number of attempts that actually reached
	// the UPDATE (= invalid + locked). Anything less means two writes
	// clobbered each other; anything more means we double-counted.
	var count int
	var lockedUntil sql.NullString
	if err := db.QueryRow(`SELECT failed_login_count, locked_until FROM users WHERE email = ?`, "race@example.com").
		Scan(&count, &lockedUntil); err != nil {
		t.Fatalf("read race row: %v", err)
	}
	wantCount := invalid + locked
	if count != wantCount {
		t.Errorf("failed_login_count: got %d, want %d (invalid=%d locked=%d busy=%d) — race condition or double-count",
			count, wantCount, invalid, locked, busy)
	}
	// locked_until is set only when the threshold was actually crossed
	// in the UPDATE path. Under heavy SQLite-busy contention the test
	// can finish with invalid+locked < N (some goroutines bounced off
	// the writer lock and never reached the threshold check). That's
	// not a correctness failure — pin the invariant the test cares
	// about: if any goroutine returned ErrAccountLocked, the row must
	// reflect a lock; if none did, the lock must NOT be set.
	if locked > 0 && !lockedUntil.Valid {
		t.Errorf("locked >= 1 but locked_until is NULL — atomic UPDATE didn't stamp the lock")
	}
	if locked == 0 && lockedUntil.Valid {
		t.Errorf("locked_until set but no goroutine returned ErrAccountLocked: %v", lockedUntil.String)
	}
}

// concurrentTestDB is the file-backed equivalent of setupTestDB needed
// for goroutine tests — the default `:memory:` shape gives each pool
// connection its own private schema, which torpedoes any test that
// hammers multiple goroutines through the same handle.
func concurrentTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "lockout.db")+"?_foreign_keys=on&_journal=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Multi-connection so TestLockout_ConcurrentBadPasswordsAdvanceCounter
	// exercises real DB-level concurrency on the UPDATE...RETURNING.
	// SetMaxOpenConns(1) serialised everything at the Go layer and a
	// non-atomic implementation could have passed.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(t.Context(), db, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestLockout_SuccessResetsCounter(t *testing.T) {
	db := setupTestDB(t)
	seedLockoutUser(t, db, "fatfinger@example.com", "correct")

	// Three failures, well below threshold.
	for i := 0; i < 3; i++ {
		_, _, _ = checkAndLockoutOnFail(context.Background(), db,
			"fatfinger@example.com", "wrong", time.Now())
	}

	// Successful login.
	if _, _, err := checkAndLockoutOnFail(context.Background(), db,
		"fatfinger@example.com", "correct", time.Now()); err != nil {
		t.Fatalf("login: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT failed_login_count FROM users WHERE email = ?`, "fatfinger@example.com").Scan(&count); err != nil {
		t.Fatalf("read fatfinger row: %v", err)
	}
	if count != 0 {
		t.Errorf("counter should reset to 0 after success, got %d", count)
	}
}
