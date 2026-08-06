package database

import (
	"strings"
	"testing"
)

// `attachments` is the single table behind every attached file (#1768 item 7).
// Five things about it are schema decisions rather than implementation details,
// and each is a production failure if it drifts.

// seedAttachmentFixture builds the chain an attachment row needs: a workspace,
// a user, a crew, an agent, an issue, a comment on it, and a chat.
func seedAttachmentFixture(t *testing.T, db *DB) {
	t.Helper()
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_att', 'WS', 'ws-att')`)
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_att2', 'WS2', 'ws-att2')`)
	execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES ('user_att', 'att@example.com')`)
	execMigrationFixture(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_att', 'ws_att', 'C', 'crew-att')`)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('agent_att', 'crew_att', 'ws_att', 'Lead', 'agent-att', 'LEAD')`)
	execMigrationFixture(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES ('msn_att', 'ws_att', 'crew_att', 'agent_att', 'trace-att', 'issue', 'BACKLOG', 'issue', datetime('now'), datetime('now'))`)
	execMigrationFixture(t, db, `INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
		VALUES ('cmt_att', 'msn_att', 'user', 'user_att', 'see the log')`)
	execMigrationFixture(t, db, `INSERT INTO chats (id, agent_id, workspace_id) VALUES ('chat_att', 'agent_att', 'ws_att')`)
}

const attSHA = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeeeffffffff0000000011111111"

// insertIssueAttachment inserts one issue-owned row. Empty arc values are
// written as NULL so a caller can build a deliberately malformed row.
func insertIssueAttachment(t *testing.T, db *DB, id, sha string) error {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO attachments (id, workspace_id, owner_type, mission_id, filename, content_type, size_bytes, sha256, storage_key)
		VALUES (?, 'ws_att', 'issue', 'msn_att', 'server.log', 'text/plain', 12, ?, ?)`,
		id, sha, "attachments/ws_att/"+sha[:2]+"/"+sha)
	return err
}

// The exclusive arc is what stops a row from decaying into an id that resolves
// to nothing. A bare (owner_type, owner_id) pair with no FK was the obvious
// shape and was rejected precisely because issues are HARD-deleted; this is the
// half of that decision the schema enforces rather than the comment describing.
func TestMigrate_Attachments_ArcRequiresExactlyOneOwner(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedAttachmentFixture(t, db)

	// Zero arcs set.
	if _, err := db.Exec(`
		INSERT INTO attachments (id, workspace_id, owner_type, filename, content_type, size_bytes, sha256, storage_key)
		VALUES ('att_none', 'ws_att', 'issue', 'f.txt', 'text/plain', 1, ?, 'k')`, attSHA); err == nil {
		t.Error("a row with no owner arc was accepted; the CHECK is what makes owner_type meaningful")
	}

	// Two arcs set — an attachment that is on an issue AND on a comment is not
	// a richer attachment, it is an ambiguous one: two different read paths
	// would both return it and a delete through either would surprise the other.
	if _, err := db.Exec(`
		INSERT INTO attachments (id, workspace_id, owner_type, mission_id, comment_id, filename, content_type, size_bytes, sha256, storage_key)
		VALUES ('att_two', 'ws_att', 'issue', 'msn_att', 'cmt_att', 'f.txt', 'text/plain', 1, ?, 'k')`, attSHA); err == nil {
		t.Error("a row with two owner arcs was accepted")
	}

	// One arc, but owner_type names a different one. Every read path filters on
	// owner_type, so a row whose discriminator disagrees with its FK is
	// invisible to the owner that actually holds it.
	if _, err := db.Exec(`
		INSERT INTO attachments (id, workspace_id, owner_type, mission_id, filename, content_type, size_bytes, sha256, storage_key)
		VALUES ('att_lie', 'ws_att', 'comment', 'msn_att', 'f.txt', 'text/plain', 1, ?, 'k')`, attSHA); err == nil {
		t.Error("owner_type='comment' with a mission_id arc was accepted; the discriminator must agree with the FK")
	}

	// …and the well-formed row is accepted, so the three refusals above are
	// about the arc and not about the fixture.
	if err := insertIssueAttachment(t, db, "att_ok", attSHA); err != nil {
		t.Fatalf("a well-formed issue attachment was refused: %v", err)
	}
}

// De-duplication is a property of the DATA, not of the caller remembering to
// check. The indexes are PARTIAL for a reason a combined UNIQUE could not
// deliver: SQLite treats NULLs as distinct, so a table-level
// UNIQUE (owner_type, mission_id, comment_id, chat_id, sha256) would never fire
// for two issue attachments (both carry comment_id IS NULL).
func TestMigrate_Attachments_SameBytesTwiceOnOneOwnerIsOneRow(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedAttachmentFixture(t, db)

	if err := insertIssueAttachment(t, db, "att_1", attSHA); err != nil {
		t.Fatalf("first attachment: %v", err)
	}
	err := insertIssueAttachment(t, db, "att_2", attSHA)
	if err == nil {
		t.Fatal("the same bytes were attached to the same issue twice; the partial UNIQUE index is " +
			"what makes a retried upload one row and one refcount rather than two")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "UNIQUE") {
		t.Errorf("second insert failed for the wrong reason: %v", err)
	}

	// Different bytes on the same owner are a second attachment, not a
	// duplicate — the constraint must not collapse those.
	other := strings.Repeat("9", 64)
	if err := insertIssueAttachment(t, db, "att_3", other); err != nil {
		t.Fatalf("a different file on the same issue was refused: %v", err)
	}
}

// Two owners holding identical bytes is the shared-blob case. It must be two
// rows: the refcount that decides whether the blob may be unlinked is exactly
// "how many rows name this sha256", so collapsing them would make the first
// delete take the second owner's file with it.
func TestMigrate_Attachments_SameBytesOnTwoOwnersAreTwoRows(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedAttachmentFixture(t, db)

	if err := insertIssueAttachment(t, db, "att_issue", attSHA); err != nil {
		t.Fatalf("issue attachment: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO attachments (id, workspace_id, owner_type, comment_id, filename, content_type, size_bytes, sha256, storage_key)
		VALUES ('att_comment', 'ws_att', 'comment', 'cmt_att', 'server.log', 'text/plain', 12, ?, ?)`,
		attSHA, "attachments/ws_att/"+attSHA[:2]+"/"+attSHA); err != nil {
		t.Fatalf("the same bytes on a comment were refused: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE sha256 = ?`, attSHA).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("rows naming the shared blob = %d, want 2", n)
	}
}

// The FK cascade is the other half of "a row never decays". An issue that is
// hard-deleted (issue_handler_update.go does exactly that for BACKLOG and
// CANCELLED) must take its attachment rows with it.
func TestMigrate_Attachments_IssueDeleteCascades(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedAttachmentFixture(t, db)
	if err := insertIssueAttachment(t, db, "att_cascade", attSHA); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable fk: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM missions WHERE id = 'msn_att'`); err != nil {
		t.Fatalf("delete issue: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM attachments WHERE id = 'att_cascade'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("the attachment survived its issue — a row pointing at a mission id that no longer " +
			"resolves is the decay the exclusive arc exists to prevent")
	}
}

// chat_attachments was never read or written by product code and is exactly
// subsumed by the owner_type='chat' arc. Leaving a dead twin beside the table
// that replaces it is the defect #1768 item 8 raises, so the migration drops
// it — and this is the assertion that keeps it dropped.
//
// workspace_files is deliberately NOT dropped (it is a path→metadata index,
// not an attachments table, and three v144 regression guards use it as their
// canary). Asserting it still EXISTS is what makes that a decision on the
// record rather than something a later reader has to infer from its absence
// from the migration.
func TestMigrate_Attachments_DropsChatAttachmentsButNotWorkspaceFiles(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chat_attachments'`).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if n != 0 {
		t.Error("chat_attachments still exists; it is exactly subsumed by attachments(owner_type='chat') " +
			"and leaving it is three attachment tables of which two are dead")
	}

	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='workspace_files'`).Scan(&n); err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	if n != 1 {
		t.Error("workspace_files was dropped; it is not an attachments table and three v144 timestamp " +
			"guards use it as their canary — dropping it belongs to #1768 item 8, with the guards moved")
	}
}

// created_at must use the ISO T-form DEFAULT. v144 converted every legacy
// space-form DEFAULT because ' ' (0x20) sorts before 'T' (0x54), so a
// legacy-form row and an RFC3339 row written by Go order wrongly in the same
// column. A new table must not reintroduce the third shape.
func TestMigrate_Attachments_CreatedAtIsTForm(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedAttachmentFixture(t, db)
	if err := insertIssueAttachment(t, db, "att_ts", attSHA); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}

	var ts string
	if err := db.QueryRow(`SELECT created_at FROM attachments WHERE id = 'att_ts'`).Scan(&ts); err != nil {
		t.Fatalf("read created_at: %v", err)
	}
	if strings.Contains(ts, " ") || !strings.HasSuffix(ts, "Z") {
		t.Errorf("created_at DEFAULT wrote %q — want the ISO T-form v144 standardised on", ts)
	}
}
