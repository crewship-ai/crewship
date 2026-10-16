package ws

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// seededDB returns a sqlite test DB pre-populated with one workspace, crew,
// agent, mission, and chat all wired to user "u-good".
func seededDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	stmts := []string{
		`INSERT INTO users(id,email) VALUES ('u-good','u-good@ws.test'),('u-bad','u-bad@ws.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES ('ws-1','WS fixture','ws-fixture')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id) VALUES ('ws-member','ws-1','u-good')`,
		`INSERT INTO crews(id,workspace_id,name,slug,deleted_at) VALUES ('crew-live','ws-1','Live','ws-live',NULL),('crew-deleted','ws-1','Deleted','ws-deleted','2024-01-01')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,deleted_at) VALUES ('agent-live','ws-1','crew-live','Live','ws-agent-live','AGENT',NULL),('agent-deleted','ws-1','crew-live','Deleted','ws-agent-deleted','AGENT','2024-01-01')`,
		`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status) VALUES ('mission-1','ws-1','crew-live','agent-live','ws-test-trace','WS fixture','TODO')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by) VALUES ('chat-1','ws-1','agent-live','u-good')`,
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
			got, err := auth.CanSubscribe(ctx, tc.userID, tc.channel)
			if err != nil {
				t.Fatalf("CanSubscribe(%q, %q): unexpected error: %v", tc.userID, tc.channel, err)
			}
			if got != tc.want {
				t.Errorf("CanSubscribe(%q, %q) = %v, want %v", tc.userID, tc.channel, got, tc.want)
			}
		})
	}
}

// A DB failure must produce (false, non-nil error): denied on the grant
// path (we never grant access on error), but flagged as NON-definitive so
// the hub's re-authorization sweep keeps existing subscriptions instead
// of mass-revoking every client on a transient DB hiccup.
func TestCanSubscribeFailsClosedOnDBError(t *testing.T) {
	t.Parallel()
	db := seededDB(t)
	auth := NewDBChannelAuthorizer(db)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ok, err := auth.CanSubscribe(context.Background(), "u-good", "workspace:ws-1")
	if ok {
		t.Error("expected deny on closed DB")
	}
	if err == nil {
		t.Error("expected a non-nil error on closed DB — a bare deny would let the sweep treat an infrastructure failure as a definitive revocation")
	}
}

// Account authority must be checked at subscription and immediately before
// queued delivery, independently of retained workspace membership.
func TestRealtimeAccountAuthorityRevocation(t *testing.T) {
	for _, state := range []string{"timestamp", "empty", "deleted"} {
		t.Run(state, func(t *testing.T) {
			db := seededDB(t)
			authorizer := NewDBChannelAuthorizer(db)
			channels := []string{"workspace:ws-1", "crew:crew-live", "agent:agent-live", "keeper:ws-1", "journal:ws-1", "files:crew-live", "mission:mission-1", "user:u-good", "providers:global"}
			for _, channel := range channels {
				if !mustVerdict(t, authorizer, "u-good", channel) {
					t.Fatalf("active account denied %s", channel)
				}
			}
			if ok, err := authorizer.CanDeliver(t.Context(), "u-good", ""); err != nil || !ok {
				t.Fatalf("active control frame: %v, %v", ok, err)
			}
			switch state {
			case "timestamp":
				if _, err := db.Exec(`UPDATE users SET suspended_at='2026-10-16T00:00:00Z' WHERE id='u-good'`); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if _, err := db.Exec(`UPDATE users SET suspended_at='' WHERE id='u-good'`); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if _, err := db.Exec(`DELETE FROM users WHERE id='u-good'`); err != nil {
					t.Fatal(err)
				}
			}
			for _, channel := range channels {
				if mustVerdict(t, authorizer, "u-good", channel) {
					t.Errorf("inactive account subscribed to %s", channel)
				}
				if ok, err := authorizer.CanDeliver(t.Context(), "u-good", channel); err != nil || ok {
					t.Errorf("inactive delivery %s: %v, %v", channel, ok, err)
				}
			}
			if ok, err := authorizer.CanDeliver(t.Context(), "u-good", ""); err != nil || ok {
				t.Errorf("inactive control frame: %v, %v", ok, err)
			}
		})
	}
}

func TestRealtimeControlDeliveryFailsClosedOnDBError(t *testing.T) {
	db := seededDB(t)
	authorizer := NewDBChannelAuthorizer(db)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if allowed, err := authorizer.CanDeliver(t.Context(), "u-good", ""); allowed || err == nil {
		t.Fatalf("closed database: allowed=%v error=%v", allowed, err)
	}
}
