package api

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
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
	db.QueryRow(`SELECT failed_login_count FROM users WHERE email = ?`, "lock1@example.com").Scan(&count)
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
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "ghost@example.com").Scan(&rowCount)
	if rowCount != 0 {
		t.Errorf("expected 0 rows for unknown email, got %d", rowCount)
	}
	var realCount int
	db.QueryRow(`SELECT failed_login_count FROM users WHERE email = ?`, "real@example.com").Scan(&realCount)
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
	db.QueryRow(`SELECT failed_login_count, locked_until FROM users WHERE email = ?`, "hostage@example.com").
		Scan(&count, &locked)
	if count != 0 {
		t.Errorf("counter not reset after success: got %d", count)
	}
	if locked.Valid {
		t.Errorf("locked_until not cleared after success: got %v", locked.String)
	}
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
	db.QueryRow(`SELECT failed_login_count FROM users WHERE email = ?`, "fatfinger@example.com").Scan(&count)
	if count != 0 {
		t.Errorf("counter should reset to 0 after success, got %d", count)
	}
}
