package database

import (
	"database/sql"
	"strings"
	"testing"
)

// mission_comment_mentions is the resolved @mention set of an issue comment
// (#1768 item 3). Four things about it are schema decisions rather than
// implementation details, and each is a production failure if it drifts.

// seedMentionFixture builds the chain a mention row needs.
func seedMentionFixture(t *testing.T, db *DB) {
	t.Helper()
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_mcm', 'WS', 'ws-mcm')`)
	execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES ('user_mcm', 'mcm@example.com')`)
	execMigrationFixture(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_mcm', 'ws_mcm', 'C', 'crew-mcm')`)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('agent_mcm_lead', 'crew_mcm', 'ws_mcm', 'Lead', 'agent-mcm-lead', 'LEAD')`)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('agent_mcm_target', 'crew_mcm', 'ws_mcm', 'Target', 'agent-mcm-target', 'MEMBER')`)
	execMigrationFixture(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES ('msn_mcm', 'ws_mcm', 'crew_mcm', 'agent_mcm_lead', 'trace-mcm', 'issue', 'BACKLOG', 'issue', datetime('now'), datetime('now'))`)
	execMigrationFixture(t, db, `INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
		VALUES ('cmt_mcm', 'msn_mcm', 'user', 'user_mcm', 'hello [@t](crewship:agent/agent_mcm_target)')`)
	execMigrationFixture(t, db, `INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat_mcm', 'agent_mcm_lead', 'ws_mcm')`)
	execMigrationFixture(t, db, `INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task)
		VALUES ('asg_mcm', 'ws_mcm', 'chat_mcm', 'agent_mcm_lead', 'agent_mcm_target', 'do it')`)
}

func insertMention(t *testing.T, db *DB, id, assignmentID string) error {
	t.Helper()
	var asg any
	if assignmentID != "" {
		asg = assignmentID
	}
	_, err := db.Exec(`
		INSERT INTO mission_comment_mentions (id, workspace_id, mission_id, comment_id, agent_id, position, dispatch_state, assignment_id)
		VALUES (?, 'ws_mcm', 'msn_mcm', 'cmt_mcm', 'agent_mcm_target', 0, 'dispatched', ?)`, id, asg)
	return err
}

// The UNIQUE (comment_id, agent_id) constraint is what makes "mentioned three
// times in one comment" one mention at the DATA level rather than only in the
// caller. The Go path de-duplicates too; this is the half that survives a
// future caller that forgets.
func TestMigrate_MissionCommentMentions_SameAgentTwiceIsOneRow(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)

	if err := insertMention(t, db, "mcm_1", "asg_mcm"); err != nil {
		t.Fatalf("first mention: %v", err)
	}
	err := insertMention(t, db, "mcm_2", "asg_mcm")
	if err == nil {
		t.Fatal("a second mention of the same agent on the same comment was accepted; " +
			"UNIQUE (comment_id, agent_id) is what collapses it to one mention, one activity row, one run")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
		t.Errorf("second insert failed for the wrong reason: %v", err)
	}
}

// Deleting the assignment must NOT be refused. `PRAGMA foreign_keys` is ON, so
// the default NO ACTION would make any assignment cleanup (retention, a purged
// run tree) fail as soon as one mention had dispatched — a silent-in-review,
// loud-in-production failure. The mention survives with a NULL link: the fact
// that the mention happened outlives the run it started.
func TestMigrate_MissionCommentMentions_AssignmentDeleteSetsNull(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)
	if err := insertMention(t, db, "mcm_asg", "asg_mcm"); err != nil {
		t.Fatalf("insert mention: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM assignments WHERE id = 'asg_mcm'`); err != nil {
		t.Fatalf("deleting the dispatched assignment was refused: %v", err)
	}
	var asg sql.NullString
	if err := db.QueryRow(
		`SELECT assignment_id FROM mission_comment_mentions WHERE id = 'mcm_asg'`).Scan(&asg); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("the mention was deleted along with its assignment; it should have survived")
		}
		t.Fatalf("read back: %v", err)
	}
	if asg.Valid {
		t.Errorf("assignment_id = %q, want NULL after the assignment was deleted", asg.String)
	}
}

// The teeth of the test above: SET NULL must not be a cascade in disguise, and
// the row must not outlive the comment it describes. Deleting the COMMENT takes
// its mentions.
func TestMigrate_MissionCommentMentions_CommentDeleteCascades(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)
	if err := insertMention(t, db, "mcm_cascade", ""); err != nil {
		t.Fatalf("insert mention: %v", err)
	}

	if _, err := db.Exec(`DELETE FROM mission_comments WHERE id = 'cmt_mcm'`); err != nil {
		t.Fatalf("delete comment: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mission_comment_mentions`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("mentions remaining after the comment was deleted = %d, want 0", n)
	}
}

// created_at's DEFAULT must be the ISO T-form, not `datetime('now')`.
//
// v144 converted every legacy space-form DEFAULT in the schema because ' '
// (0x20) sorts before 'T' (0x54), so a DEFAULT-written row and a Go-written
// RFC3339 row in the same column order wrong. A new table that reintroduces the
// space form reopens that bug for itself, and v144 has already run — nothing
// will convert it after the fact.
func TestMigrate_MissionCommentMentions_CreatedAtDefaultIsTForm(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)

	if _, err := db.Exec(`
		INSERT INTO mission_comment_mentions (id, workspace_id, mission_id, comment_id, agent_id)
		VALUES ('mcm_default', 'ws_mcm', 'msn_mcm', 'cmt_mcm', 'agent_mcm_target')`); err != nil {
		t.Fatalf("insert with default created_at: %v", err)
	}
	var createdAt string
	if err := db.QueryRow(
		`SELECT created_at FROM mission_comment_mentions WHERE id = 'mcm_default'`).Scan(&createdAt); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if strings.Contains(createdAt, " ") || !strings.Contains(createdAt, "T") || !strings.HasSuffix(createdAt, "Z") {
		t.Errorf("created_at default wrote %q — want ISO T-form (…T…Z). The legacy space form sorts "+
			"before every RFC3339 value Go writes into the same column", createdAt)
	}
}
