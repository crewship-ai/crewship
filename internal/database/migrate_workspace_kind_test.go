package database

import "testing"

// The backfill marks only what sign-up made and nobody changed since: one
// member, its owner, named "<their name>'s Workspace". A shared or renamed one
// is a team workspace — that is what it has become.
func TestWorkspaceKindMigration_BackfillsSignUpWorkspaces(t *testing.T) {
	db := migrationBoundaryDB(t,
		`CREATE TABLE workspaces(id TEXT PRIMARY KEY, name TEXT NOT NULL);
		 CREATE TABLE users(id TEXT PRIMARY KEY, full_name TEXT);
		 CREATE TABLE workspace_members(id TEXT PRIMARY KEY, workspace_id TEXT, user_id TEXT, role TEXT);`)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users VALUES ('ivana','Ivana Viewer'),('demo','Demo User'),('tom','Tom'),('ann','Ann'),('nameless',NULL)`)
	for _, ws := range []struct{ id, name string }{
		{"personal", "Ivana Viewer's Workspace"},
		{"shared", "Demo User's Workspace"},
		{"renamed", "Tom's lab"},
		{"team", "Test"},
		{"not-owner", "Ann's Workspace"},
		{"nameless", "'s Workspace"},
	} {
		exec(`INSERT INTO workspaces VALUES (?, ?)`, ws.id, ws.name)
	}
	exec(`INSERT INTO workspace_members VALUES
		('m1','personal','ivana','OWNER'),
		('m2','shared','demo','OWNER'), ('m3','shared','ivana','VIEWER'),
		('m4','renamed','tom','OWNER'),
		('m5','team','demo','OWNER'),
		('m6','not-owner','ann','ADMIN'),
		('m7','nameless','nameless','OWNER')`)

	migration, err := migrationFS.ReadFile("migrations/20261012211544_workspace_kind.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(migration))

	for id, want := range map[string]string{
		"personal": "personal", "shared": "team", "renamed": "team", "team": "team", "not-owner": "team", "nameless": "personal",
	} {
		var kind string
		if err := db.QueryRow(`SELECT kind FROM workspaces WHERE id = ?`, id).Scan(&kind); err != nil {
			t.Fatal(err)
		}
		if kind != want {
			t.Errorf("%s: kind = %q, want %q", id, kind, want)
		}
	}
	if _, err := db.Exec(`UPDATE workspaces SET kind = 'other' WHERE id = 'team'`); err == nil {
		t.Error("kind accepted a value other than team or personal")
	}
}
