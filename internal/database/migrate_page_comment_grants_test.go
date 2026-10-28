package database

import (
	"database/sql"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestPageCommentGrantMigrationPreservesAuthorityAndRejectsAgents(t *testing.T) {
	db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, 20261028193409, logger); err != nil {
		t.Fatal(err)
	}
	beforeSchema := commentGrantDependentSchema(t, db)
	mustExec(t, db, `INSERT INTO users(id,email,full_name) VALUES('comment-owner','comment-owner@example.test','Owner'),('comment-user','comment-user@example.test','Commenter')`)
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('comment-ws','Comment','comment-ws')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('comment-page','comment-ws','comment-page','Page','comment-owner','{}')`)
	for _, level := range []string{"read", "produce", "write"} {
		if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('comment-page','user','comment-user',?,'comment-owner')`, level); err != nil {
			t.Fatalf("valid %s grant refused: %v", level, err)
		}
	}
	var epochBefore, epochAfter int64
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='comment-ws'`).Scan(&epochBefore); err != nil {
		t.Fatal(err)
	}
	migration, err := findMigration(20261028193410)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), migration.sql); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if afterSchema := commentGrantDependentSchema(t, db); !reflect.DeepEqual(beforeSchema, afterSchema) {
		t.Fatalf("authority trigger/view definitions changed: before=%v after=%v", beforeSchema, afterSchema)
	}
	if err := db.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='comment-ws'`).Scan(&epochAfter); err != nil || epochAfter != epochBefore {
		t.Fatalf("migration changed authority epoch %d -> %d: %v", epochBefore, epochAfter, err)
	}
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('comment-page','user','comment-user','comment','comment-owner')`); err != nil {
		t.Fatalf("valid comment grant refused: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('comment-page','agent','not-an-agent','comment','comment-owner')`,
		`UPDATE page_grants SET subject_type='agent' WHERE level='comment'`,
		`UPDATE page_grants SET panel_ids='["x"]' WHERE level='comment'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("invalid comment grant accepted: %s", query)
		}
	}
	mustExec(t, db, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) VALUES('comment-ws','user','comment-user')`)
	var before, after int64
	if err := db.QueryRow(`SELECT revision FROM mooring_authority_generations WHERE workspace_id='comment-ws' AND scope_kind='user' AND scope_id='comment-user'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `DELETE FROM page_grants WHERE level='comment'`)
	if err := db.QueryRow(`SELECT revision FROM mooring_authority_generations WHERE workspace_id='comment-ws' AND scope_kind='user' AND scope_id='comment-user'`).Scan(&after); err != nil || after <= before {
		t.Fatalf("grant revoke lost non-ABA fence: %d -> %d (%v)", before, after, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM page_grants`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("existing read/produce/write grants lost: %d %v", count, err)
	}
	mustExec(t, db, `DELETE FROM pages WHERE id='comment-page'`)
	if err := db.QueryRow(`SELECT count(*) FROM page_grants`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Page FK cascade lost: %d %v", count, err)
	}
}

func commentGrantDependentSchema(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name,sql FROM sqlite_master WHERE type IN ('trigger','view','index') AND (sql LIKE '%page_grants%' OR sql LIKE '%mooring_generation_grant_recipients%') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		out[name] = strings.Join(strings.Fields(definition), " ")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) < 24 {
		t.Fatalf("missing dependent definitions: %d", len(out))
	}
	return out
}
