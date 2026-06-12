package sessions

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newBareDB opens a SQLite database WITHOUT running migrations, so
// every statement against user_sessions fails with "no such table".
// Used to exercise the SQL-error branches of the store.
func newBareDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+dir+"/bare.db?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// insertRawSession bypasses Create so tests can plant malformed rows.
func insertRawSession(t *testing.T, db *sql.DB, id, createdAt, expiresAt, lastUsedAt string, revokedAt any) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO user_sessions
		(id, user_id, created_at, expires_at, last_used_at, revoked_at)
		VALUES (?, 'u1', ?, ?, ?, ?)`,
		id, createdAt, expiresAt, lastUsedAt, revokedAt)
	if err != nil {
		t.Fatalf("insert raw session: %v", err)
	}
}

func TestCreate_InsertError(t *testing.T) {
	store := NewDBStore(newBareDB(t)) // no user_sessions table
	_, err := store.Create(context.Background(), "u1", "UA", "1.2.3.4", time.Hour)
	if err == nil {
		t.Fatal("expected error inserting into missing table")
	}
	if !strings.Contains(err.Error(), "insert session") {
		t.Errorf("error should be wrapped with 'insert session', got: %v", err)
	}
}

func TestListActiveForUser_QueryError(t *testing.T) {
	store := NewDBStore(newBareDB(t))
	_, err := store.ListActiveForUser(context.Background(), "u1")
	if err == nil {
		t.Fatal("expected query error against missing table")
	}
	if !strings.Contains(err.Error(), "query sessions") {
		t.Errorf("error should be wrapped with 'query sessions', got: %v", err)
	}
}

func TestListActiveForUser_ScanErrorOnCorruptRow(t *testing.T) {
	db := newTestDB(t)
	store := NewDBStore(db)

	// Active row (revoked_at NULL, expires_at far future) but with a
	// garbage created_at — scanSession must surface corruption loudly.
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	valid := time.Now().UTC().Format(time.RFC3339)
	insertRawSession(t, db, "s_corrupt", "not-a-timestamp", future, valid, nil)

	_, err := store.ListActiveForUser(context.Background(), "u1")
	if err == nil {
		t.Fatal("expected scan error for corrupt created_at")
	}
	if !strings.Contains(err.Error(), "bad created_at") {
		t.Errorf("expected 'bad created_at' in error, got: %v", err)
	}
}

func TestRevoke_EmptyID(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	err := store.Revoke(context.Background(), "", ReasonLogout)
	if err == nil || !strings.Contains(err.Error(), "session id required") {
		t.Fatalf("expected 'session id required' error, got: %v", err)
	}
}

func TestRevoke_ExecError(t *testing.T) {
	store := NewDBStore(newBareDB(t))
	err := store.Revoke(context.Background(), "s_x", ReasonLogout)
	if err == nil {
		t.Fatal("expected exec error against missing table")
	}
	if !strings.Contains(err.Error(), "revoke session") {
		t.Errorf("error should be wrapped with 'revoke session', got: %v", err)
	}
}

func TestRevokeAllForUser_EmptyUserID(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	_, err := store.RevokeAllForUser(context.Background(), "", ReasonAdminForce)
	if err == nil || !strings.Contains(err.Error(), "user id required") {
		t.Fatalf("expected 'user id required' error, got: %v", err)
	}
}

func TestRevokeAllForUser_ExecError(t *testing.T) {
	store := NewDBStore(newBareDB(t))
	_, err := store.RevokeAllForUser(context.Background(), "u1", ReasonAdminForce)
	if err == nil {
		t.Fatal("expected exec error against missing table")
	}
	if !strings.Contains(err.Error(), "revoke all") {
		t.Errorf("error should be wrapped with 'revoke all', got: %v", err)
	}
}

func TestTouchLastUsed_EmptyID(t *testing.T) {
	store := NewDBStore(newTestDB(t))
	if err := store.TouchLastUsed(context.Background(), ""); err != nil {
		t.Fatalf("empty id must be a silent no-op, got: %v", err)
	}
}

func TestTouchLastUsed_SQLErrorRollsBackThrottleCache(t *testing.T) {
	store := NewDBStore(newBareDB(t)) // every UPDATE fails
	fixed := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return fixed })

	ctx := context.Background()
	err := store.TouchLastUsed(ctx, "s_x")
	if err == nil {
		t.Fatal("expected SQL error against missing table")
	}
	if !strings.Contains(err.Error(), "touch last_used") {
		t.Errorf("error should be wrapped with 'touch last_used', got: %v", err)
	}

	// The failed write must NOT poison the throttle cache: an
	// immediate retry (same clock instant, well within the 60s
	// throttle) must hit the DB again and fail again — not return a
	// silent nil from the throttle path.
	err = store.TouchLastUsed(ctx, "s_x")
	if err == nil {
		t.Fatal("second touch returned nil — failed write was cached as a success")
	}
}

func TestGet_CorruptTimestampRows(t *testing.T) {
	good := time.Now().UTC().Format(time.RFC3339)
	cases := []struct {
		name       string
		id         string
		createdAt  string
		expiresAt  string
		lastUsedAt string
		revokedAt  any
		wantSubstr string
	}{
		{"bad expires_at", "s_bad_exp", good, "garbage", good, nil, "bad expires_at"},
		{"bad last_used_at", "s_bad_lu", good, good, "garbage", nil, "bad last_used_at"},
		{"bad revoked_at", "s_bad_rev", good, good, good, "garbage", "bad revoked_at"},
	}

	db := newTestDB(t)
	store := NewDBStore(db)
	ctx := context.Background()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			insertRawSession(t, db, tc.id, tc.createdAt, tc.expiresAt, tc.lastUsedAt, tc.revokedAt)
			_, err := store.Get(ctx, tc.id)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("expected %q in error, got: %v", tc.wantSubstr, err)
			}
			// Corruption must never be confused with absence.
			if errors.Is(err, ErrNotFound) {
				t.Errorf("corrupt row must not map to ErrNotFound: %v", err)
			}
		})
	}
}
