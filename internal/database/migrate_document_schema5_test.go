package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestMigrationDocumentSchema5AgentPipeline (#5233): the rebuild of
// document_iteration_intents and document_proposals keeps every existing row
// and charges no storage twice, keeps the immutability and storage triggers,
// and widens only the closed schema CHECKs to schema 5.
func TestMigrationDocumentSchema5AgentPipeline(t *testing.T) {
	const before, after = 20261108204553, 20261109090843
	if _, err := findMigration(after); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version > before && m.version < after {
			t.Fatalf("migration %d sits between; seed it too", m.version)
		}
	}
	// One connection with foreign keys off: the seeded rows name no real
	// workspace, chat or run; this test is about the rebuild, not the FKs.
	db, err := sql.Open("sqlite", fmt.Sprintf("file:crewship-migrate-doc5-%d?mode=memory&cache=shared&_pragma=foreign_keys(OFF)", migrateV89Counter.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(ctx, db, before, logger); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	intent := `INSERT INTO document_iteration_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,page_id,schema_version,document_generation,original_rev,draft_rev,pages_version,policy_version,member_id,member_revision,chat_generation,chat_revision,scope_blocks) VALUES(?,'w','u','c',?,'p',?,0,1,1,1,1,'m',1,'g',1,?)`
	proposal := `INSERT INTO document_proposals(workspace_id,page_id,id,run_id,actor,target,ancestors,schema_version,generation,base_rev,base_hash,proposed_hash,text,base_json,proposed_json) VALUES('w','p',?,'r','agent:a:r','lead','[]',?,0,1,?,?,'text','{}','{}')`
	hash := strings.Repeat("a", 64)
	for _, err := range []error{
		exec(intent, "m3", "s3", 3, nil),
		exec(intent, "m4", "s4", 4, `["lead"]`),
		exec(proposal, "p4-open", 4, hash, hash),
		exec(proposal, "p4-done", 4, hash, hash),
		exec(`UPDATE document_proposals SET status='accepted',decided_by='user:u',decision_id='d',decided_at='2026-11-09T00:00:00Z',accepted_rev=2 WHERE id='p4-done'`),
	} {
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if exec(intent, "m5", "s5", 5, nil) == nil || exec(proposal, "p5", 5, hash, hash) == nil {
		t.Fatal("schema 5 admitted before the migration")
	}
	snapshot := func(query string) string {
		t.Helper()
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, _ := rows.Columns()
		var out strings.Builder
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&out, values...)
		}
		return out.String()
	}
	const intents = `SELECT * FROM document_iteration_intents ORDER BY message_id`
	const proposals = `SELECT * FROM document_proposals ORDER BY id`
	const charges = `SELECT coalesce((SELECT used_bytes FROM artifact_storage_workspaces WHERE workspace_id='w'),-1),(SELECT used_bytes FROM artifact_storage_global WHERE id=1)`
	wantIntents, wantProposals, wantCharges := snapshot(intents), snapshot(proposals), snapshot(charges)
	m, _ := findMigration(after)
	if m.sql == "" {
		t.Fatal("the migration is not plain SQL")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := snapshot(intents); got != wantIntents {
		t.Fatalf("intents changed:\n%s\nwant\n%s", got, wantIntents)
	}
	if got := snapshot(proposals); got != wantProposals {
		t.Fatalf("proposals changed:\n%s\nwant\n%s", got, wantProposals)
	}
	if got := snapshot(charges); got != wantCharges {
		t.Fatalf("the rebuild charged storage: %s, want %s", got, wantCharges)
	}
	objects := snapshot(`SELECT name FROM sqlite_master WHERE tbl_name IN ('document_iteration_intents','document_proposals') AND type IN ('index','trigger') AND name NOT LIKE 'sqlite_autoindex%' ORDER BY name`)
	for _, name := range []string{"document_iteration_page", "document_iteration_chat", "document_iteration_human", "document_iteration_identity_immutable", "document_iteration_binding_immutable", "document_iteration_cursor_monotonic", "idx_document_proposals_status", "document_proposals_immutable", "document_proposals_decided_once", "document_proposals_storage_insert", "document_proposals_storage_delete", "document_proposals_storage_update"} {
		if !strings.Contains(objects, name+"\n") {
			t.Errorf("missing %s after the rebuild: %s", name, objects)
		}
	}
	for _, check := range []struct {
		name string
		err  error
		want bool
	}{
		{"schema-5 mandate with blocks", exec(intent, "m5", "s5", 5, `["lead"]`), true},
		{"schema-3 mandate with blocks", exec(intent, "m3b", "s3b", 3, `["lead"]`), false},
		{"schema-6 mandate", exec(intent, "m6", "s6", 6, nil), false},
		{"schema-5 mandate binds a run", exec(`UPDATE document_iteration_intents SET run_id='r5',expires_at='x' WHERE message_id='m5'`), true},
		{"schema-5 propose cursor never moves", exec(`UPDATE document_iteration_intents SET draft_rev=2 WHERE message_id='m5'`), false},
		{"schema-5 proposal", exec(proposal, "p5", 5, hash, hash), true},
		{"schema-3 proposal", exec(proposal, "p3", 3, hash, hash), false},
		{"schema-6 proposal", exec(proposal, "p6", 6, hash, hash), false},
		{"proposal content is immutable", exec(`UPDATE document_proposals SET text='other' WHERE id='p5'`), false},
		{"a decided proposal stays decided", exec(`UPDATE document_proposals SET status='rejected',decided_by='x',decision_id='y' WHERE id='p4-done'`), false},
	} {
		if (check.err == nil) != check.want {
			t.Errorf("%s: err=%v, want success=%v", check.name, check.err, check.want)
		}
	}
	if got := snapshot(charges); got == wantCharges {
		t.Fatalf("a new schema-5 proposal is not charged: %s", got)
	}
}
