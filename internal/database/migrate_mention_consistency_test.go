package database

import (
	"strings"
	"testing"
)

// seedForeignTenantForMentions adds a second workspace with its own crew,
// agent, mission and comment. Everything in it is a valid FK target, which is
// the whole point: the four ids on a mention row each resolve, and before the
// consistency triggers nothing checked that they resolved to the same tenant.
func seedForeignTenantForMentions(t *testing.T, db *DB) {
	t.Helper()
	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_other', 'Other', 'ws-other')`)
	execMigrationFixture(t, db, `INSERT INTO users (id, email) VALUES ('user_other', 'other@example.com')`)
	execMigrationFixture(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_other', 'ws_other', 'O', 'crew-other')`)
	execMigrationFixture(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug, agent_role)
		VALUES ('agent_other', 'crew_other', 'ws_other', 'Other Lead', 'agent-other', 'LEAD')`)
	execMigrationFixture(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, created_at, updated_at)
		VALUES ('msn_other', 'ws_other', 'crew_other', 'agent_other', 'trace-other', 'other issue', 'BACKLOG', 'issue', datetime('now'), datetime('now'))`)
	execMigrationFixture(t, db, `INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
		VALUES ('cmt_other', 'msn_other', 'user', 'user_other', 'hi')`)
}

// TestMigrate_MentionConsistency_RejectsMismatchedTenants is the data-level
// half of the mention trigger's contract.
//
// Every column on mission_comment_mentions carries an FK, so each id resolves.
// None of them proved the ids resolve TOGETHER — a row could name one tenant's
// comment, another's mission and a third's agent and satisfy every constraint.
// The Go writer derives all four from one context, so they agree today; this
// pins the invariant against the caller that has not been written yet, which is
// the failure mode this whole change exists to stop.
//
// Each case changes exactly ONE id, so a passing case cannot be explained by a
// different check firing.
func TestMigrate_MentionConsistency_RejectsMismatchedTenants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		workspace  string
		mission    string
		comment    string
		agent      string
		wantErrHas string
	}{
		{
			name:      "a comment from another mission",
			workspace: "ws_mcm", mission: "msn_mcm", comment: "cmt_other", agent: "agent_mcm_target",
			wantErrHas: "comment must belong to the mention mission",
		},
		{
			name:      "a mission from another workspace",
			workspace: "ws_mcm", mission: "msn_other", comment: "cmt_other", agent: "agent_mcm_target",
			wantErrHas: "mission must belong to the mention workspace",
		},
		{
			name:      "an agent from another workspace",
			workspace: "ws_mcm", mission: "msn_mcm", comment: "cmt_mcm", agent: "agent_other",
			wantErrHas: "agent must belong to the mention workspace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := migrateChainSetup(t)
			seedMentionFixture(t, db)
			seedForeignTenantForMentions(t, db)

			_, err := db.Exec(`
				INSERT INTO mission_comment_mentions
				  (id, workspace_id, mission_id, comment_id, agent_id, position, dispatch_state)
				VALUES ('mcm_bad', ?, ?, ?, ?, 0, 'dispatched')`,
				tt.workspace, tt.mission, tt.comment, tt.agent)
			if err == nil {
				t.Fatalf("a mention row mixing tenants was accepted (%s)", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErrHas) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantErrHas)
			}

			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM mission_comment_mentions`).Scan(&n); err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != 0 {
				t.Errorf("rows after the refused insert = %d, want 0", n)
			}
		})
	}
}

// TestMigrate_MentionConsistency_AllowsTheRealShape is the guard on the
// over-correction: the triggers must not cost the ordinary write. This is the
// exact shape mentionRecorder.record produces, and if it ever stops being
// accepted the feature is broken rather than hardened.
func TestMigrate_MentionConsistency_AllowsTheRealShape(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)
	seedForeignTenantForMentions(t, db)

	if err := insertMention(t, db, "mcm_ok", "asg_mcm"); err != nil {
		t.Fatalf("the consistent shape must still insert: %v", err)
	}

	// dispatch_state is written after the row exists, so the UPDATE trigger is
	// on the live path too — a trigger that only guarded INSERT would let an
	// update move the row to another tenant.
	if _, err := db.Exec(
		`UPDATE mission_comment_mentions SET dispatch_state = 'refused', dispatch_detail = 'cap' WHERE id = 'mcm_ok'`); err != nil {
		t.Fatalf("the ordinary dispatch-state update must still succeed: %v", err)
	}
}

// TestMigrate_MentionConsistency_UpdateCannotMoveTenant covers the half an
// INSERT-only trigger would miss.
func TestMigrate_MentionConsistency_UpdateCannotMoveTenant(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)
	seedMentionFixture(t, db)
	seedForeignTenantForMentions(t, db)

	if err := insertMention(t, db, "mcm_ok", "asg_mcm"); err != nil {
		t.Fatalf("seed mention: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE mission_comment_mentions SET agent_id = 'agent_other' WHERE id = 'mcm_ok'`); err == nil {
		t.Fatal("an UPDATE moved the mention to another tenant's agent")
	}
}
