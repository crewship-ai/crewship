package ws

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// seededDB returns a sqlite test DB pre-populated with one workspace, crew,
// agent, mission, and chat all wired to user "u-good".
func seededDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

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
	stmts := []string{
		`INSERT INTO workspace_members(workspace_id, user_id) VALUES ('ws-1', 'u-good')`,
		`INSERT INTO crews(id, workspace_id, deleted_at) VALUES ('crew-live', 'ws-1', NULL)`,
		`INSERT INTO crews(id, workspace_id, deleted_at) VALUES ('crew-deleted', 'ws-1', '2024-01-01')`,
		`INSERT INTO agents(id, workspace_id, deleted_at) VALUES ('agent-live', 'ws-1', NULL)`,
		`INSERT INTO agents(id, workspace_id, deleted_at) VALUES ('agent-deleted', 'ws-1', '2024-01-01')`,
		`INSERT INTO missions(id, crew_id) VALUES ('mission-1', 'crew-live')`,
		`INSERT INTO chats(id, workspace_id) VALUES ('chat-1', 'ws-1')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	return db
}

// Table-driven matrix over every supported channel type — covers the
// happy path and the "wrong owner / nonexistent target" denials in one place.
func TestCanSubscribeMatrix(t *testing.T) {
	t.Parallel()
	db := seededDB(t)
	auth := NewDBChannelAuthorizer(db)
	ctx := context.Background()

	tests := []struct {
		name    string
		userID  string
		channel string
		want    bool
	}{
		// Workspace.
		{"workspace allowed", "u-good", "workspace:ws-1", true},
		{"workspace stranger denied", "u-bad", "workspace:ws-1", false},
		{"workspace missing denied", "u-good", "workspace:ws-missing", false},

		// Crew (joins workspace_members via crews).
		{"crew allowed", "u-good", "crew:crew-live", true},
		{"crew deleted denied", "u-good", "crew:crew-deleted", false},
		{"crew missing denied", "u-good", "crew:nope", false},
		{"crew stranger denied", "u-bad", "crew:crew-live", false},

		// Agent (joins workspace_members via agents).
		{"agent allowed", "u-good", "agent:agent-live", true},
		{"agent deleted denied", "u-good", "agent:agent-deleted", false},
		{"agent missing denied", "u-good", "agent:nope", false},

		// Session (chats join workspace_members).
		{"session allowed", "u-good", "session:chat-1", true},
		{"session missing denied", "u-good", "session:nope", false},
		{"session stranger denied", "u-bad", "session:chat-1", false},

		// Keeper (alias for workspace).
		{"keeper allowed", "u-good", "keeper:ws-1", true},
		{"keeper stranger denied", "u-bad", "keeper:ws-1", false},

		// Files (alias for crew workspace).
		{"files allowed", "u-good", "files:crew-live", true},
		{"files missing crew denied", "u-good", "files:nope", false},

		// Mission (joins crews→workspace_members via missions).
		{"mission allowed", "u-good", "mission:mission-1", true},
		{"mission missing denied", "u-good", "mission:nope", false},

		// Providers — global, any authenticated user.
		{"providers any user", "u-good", "providers:global", true},
		{"providers any other user", "u-bad", "providers:global", true},

		// Unknown channel type denied.
		{"unknown type denied", "u-good", "unknown:x", false},

		// Malformed.
		{"no colon denied", "u-good", "noseparator", false},
		{"empty id denied", "u-good", "workspace:", false},
		{"empty userID denied", "", "workspace:ws-1", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := auth.CanSubscribe(ctx, tc.userID, tc.channel)
			if got != tc.want {
				t.Errorf("CanSubscribe(%q, %q) = %v, want %v", tc.userID, tc.channel, got, tc.want)
			}
		})
	}
}

// Closed DB must produce false (defensive — we never grant access on error).
func TestCanSubscribeFailsClosedOnDBError(t *testing.T) {
	t.Parallel()
	db := seededDB(t)
	auth := NewDBChannelAuthorizer(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if auth.CanSubscribe(context.Background(), "u-good", "workspace:ws-1") {
		t.Error("expected deny on closed DB")
	}
}
