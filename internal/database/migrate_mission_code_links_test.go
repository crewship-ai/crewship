package database

import (
	"database/sql"
	"testing"
)

// mission_code_links carries two authorship FKs — created_by_user_id and
// created_by_agent_id — and what happens to them when the referenced row is
// deleted is a schema decision, not an implementation detail.
//
// `PRAGMA foreign_keys` is ON for every connection (see Open), so the default
// NO ACTION is not "nothing happens": it makes the PARENT delete fail. An
// Art. 17 erasure through DELETE /api/v1/admin/users/{userId}/data (the v107
// GDPR cascade) hard-deletes the data subject, and one attached pull request
// would be enough to refuse it. That failure mode is silent in review and loud
// in production, which is why it is pinned here.
//
// The link itself is not personal data, so SET NULL is the right loss: the row
// survives without an attributed author.

// seedCodeLinkFKFixture builds the chain a mission_code_links row needs and
// returns the ids of the author user and agent.
func seedCodeLinkFKFixture(t *testing.T, db *DB) (userID, agentID string) {
	t.Helper()
	userID, agentID = "user_mcl", "agent_mcl_author"
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_mcl', 'WS', 'ws-mcl')`)
	execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES (?, 'mcl@example.com')`, userID)
	execMigrationFixture(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_mcl', 'ws_mcl', 'C', 'crew-mcl')`)
	// Two agents: the issue's lead, and the one that attached the link. They
	// have to be different rows — missions.lead_agent_id is NOT NULL, so the
	// lead cannot be deleted at all, and reusing it would make this test fail
	// for a reason that has nothing to do with the authorship FK.
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('agent_mcl_lead', 'crew_mcl', 'ws_mcl', 'Lead', 'agent-mcl-lead', 'LEAD')`)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES (?, 'crew_mcl', 'ws_mcl', 'Author', 'agent-mcl-author', 'MEMBER')`, agentID)
	execMigrationFixture(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES ('msn_mcl', 'ws_mcl', 'crew_mcl', 'agent_mcl_lead', 'trace-mcl', 'issue', 'BACKLOG', 'issue', datetime('now'), datetime('now'))`)
	return userID, agentID
}

// insertCodeLink writes one link. `number` varies per call because
// UNIQUE (mission_id, provider, host, owner, repo, number) is the duplicate
// guard — two links on one issue have to point at different pull requests.
func insertCodeLink(t *testing.T, db *DB, id string, number int, userID, agentID string) {
	t.Helper()
	execMigrationFixture(t, db, `
		INSERT INTO mission_code_links (id, workspace_id, mission_id, provider, host, owner, repo, number, kind, url,
		                                created_by_user_id, created_by_agent_id)
		VALUES (?, 'ws_mcl', 'msn_mcl', 'GITHUB', 'github.com', 'acme', 'thing', ?, 'pull_request',
		        'https://github.com/acme/thing/pull/' || ?, ?, ?)`,
		id, number, number, nullable(userID), nullable(agentID))
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Deleting the author must not be refused, and must not take the link with it.
func TestMigrate_MissionCodeLinks_AuthorDeleteSetsNull(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	userID, agentID := seedCodeLinkFKFixture(t, db)
	insertCodeLink(t, db, "lnk_user", 7, userID, "")
	insertCodeLink(t, db, "lnk_agent", 8, "", agentID)

	// This is the assertion that fails outright under NO ACTION: the parent
	// delete errors with FOREIGN KEY constraint failed, and an erasure request
	// is refused because somebody linked a pull request.
	if _, err := db.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatalf("deleting the author user was refused: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM agents WHERE id = ?`, agentID); err != nil {
		t.Fatalf("deleting the author agent was refused: %v", err)
	}

	for _, tc := range []struct{ id, column string }{
		{"lnk_user", "created_by_user_id"},
		{"lnk_agent", "created_by_agent_id"},
	} {
		var author sql.NullString
		err := db.QueryRow(
			`SELECT `+tc.column+` FROM mission_code_links WHERE id = ?`, tc.id).Scan(&author)
		if err == sql.ErrNoRows {
			t.Fatalf("%s: the link was deleted along with its author; it should have survived", tc.id)
		}
		if err != nil {
			t.Fatalf("%s: read back: %v", tc.id, err)
		}
		if author.Valid {
			t.Errorf("%s: %s = %q, want NULL after the author was deleted", tc.id, tc.column, author.String)
		}
	}
}

// The teeth of the test above: SET NULL must not be a cascade in disguise.
// Deleting the ISSUE still takes its links (ON DELETE CASCADE on mission_id),
// so the previous test is pinning the authorship action specifically and not
// "rows survive deletes".
func TestMigrate_MissionCodeLinks_IssueDeleteCascades(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	userID, _ := seedCodeLinkFKFixture(t, db)
	insertCodeLink(t, db, "lnk_cascade", 9, userID, "")

	if _, err := db.Exec(`DELETE FROM missions WHERE id = 'msn_mcl'`); err != nil {
		t.Fatalf("delete mission: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_code_links WHERE id = 'lnk_cascade'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("links remaining after the issue was deleted = %d, want 0", n)
	}
}
