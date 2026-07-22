package ws

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestNewDBChannelAuthorizer_PanicsOnNilDB is a regression for the CodeRabbit
// finding on PR #130: passing a nil *sql.DB to NewDBChannelAuthorizer used to
// succeed, and a later CanSubscribe call would dereference the nil handle.
// The constructor now fails fast; this test guards that behavior.
func TestNewDBChannelAuthorizer_PanicsOnNilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil *sql.DB, got none")
		}
	}()
	_ = NewDBChannelAuthorizer(nil)
}

// TestDBChannelAuthorizer_CanSubscribeFailClosed verifies the belt-and-braces
// nil checks inside CanSubscribe: even if someone synthesizes a zero-value
// DBChannelAuthorizer (bypassing the constructor), the method must fail
// closed rather than panic.
func TestDBChannelAuthorizer_CanSubscribeFailClosed(t *testing.T) {
	// These are all STRUCTURAL denials — they must come back as a
	// definitive (false, nil), not an error, or the hub's re-auth sweep
	// would treat them as transient and never act on them.
	var a *DBChannelAuthorizer // nil receiver
	if mustVerdict(t, a, "u1", "workspace:ws1") {
		t.Error("nil receiver should deny")
	}

	zero := &DBChannelAuthorizer{} // zero value, no db
	if mustVerdict(t, zero, "u1", "workspace:ws1") {
		t.Error("zero-value authorizer should deny")
	}

	// Empty userID and malformed channel strings also deny.
	db := openTestDB(t)
	defer db.Close()
	auth := NewDBChannelAuthorizer(db)

	if mustVerdict(t, auth, "", "workspace:ws1") {
		t.Error("empty userID should deny")
	}
	if mustVerdict(t, auth, "u1", "no-colon") {
		t.Error("channel without type:id should deny")
	}
	if mustVerdict(t, auth, "u1", "workspace:") {
		t.Error("channel with empty id should deny")
	}
}

// mustVerdict calls CanSubscribe and fails the test if the result is not
// definitive (err != nil). Used where the test's subject is the verdict,
// not the error path.
func mustVerdict(t *testing.T, a *DBChannelAuthorizer, userID, channel string) bool {
	t.Helper()
	ok, err := a.CanSubscribe(context.Background(), userID, channel)
	if err != nil {
		t.Fatalf("CanSubscribe(%q, %q): unexpected error: %v", userID, channel, err)
	}
	return ok
}

// TestDBChannelAuthorizer_UserChannel covers the user:{userId} channel
// (issue #614): a user may subscribe to their own channel but not another
// user's, and the check needs no DB membership lookup. Before the fix this
// channel fell through to default:false so notification.created broadcasts
// were never delivered over WS.
func TestDBChannelAuthorizer_UserChannel(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	auth := NewDBChannelAuthorizer(db)

	if !mustVerdict(t, auth, "u1", "user:u1") {
		t.Error("user should be allowed to subscribe to their own channel")
	}
	if mustVerdict(t, auth, "u1", "user:u2") {
		t.Error("user must not subscribe to another user's channel")
	}
}

// openTestDB returns a minimal SQLite DB for authorizer tests. It only needs
// the schema objects CanSubscribe reads from (workspace_members and friends),
// which we create directly instead of wiring the whole migration runner.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	schema := `
		CREATE TABLE workspace_members (
			workspace_id TEXT NOT NULL,
			user_id TEXT NOT NULL
		);
		CREATE TABLE crews (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			deleted_at TEXT
		);
		CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			deleted_at TEXT
		);
		CREATE TABLE missions (
			id TEXT PRIMARY KEY,
			crew_id TEXT NOT NULL
		);
		CREATE TABLE chats (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL
		);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}
