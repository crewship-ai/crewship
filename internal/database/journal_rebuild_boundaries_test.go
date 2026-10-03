package database

import (
	"context"
	"strings"
	"testing"
)

func TestJournalRebuildFailurePreservesAuditAndConnectionPolicy(t *testing.T) {
	const original = `CREATE TABLE journal_entries(id TEXT PRIMARY KEY, workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE, crew_id TEXT REFERENCES crews(id) ON DELETE SET NULL, agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL, mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL, summary TEXT)`
	for _, failure := range []string{"invalid schema", "column mismatch", "copy refused", "broken fts"} {
		t.Run(failure, func(t *testing.T) {
			db := migrationBoundaryDB(t, `CREATE TABLE workspaces(id TEXT PRIMARY KEY); CREATE TABLE crews(id TEXT PRIMARY KEY); CREATE TABLE agents(id TEXT PRIMARY KEY); CREATE TABLE missions(id TEXT PRIMARY KEY)`, original, `INSERT INTO journal_entries(id,summary) VALUES('entry','immutable audit'); CREATE INDEX audit_summary ON journal_entries(summary); PRAGMA foreign_keys=ON`)
			ddl := original
			want := ""
			switch failure {
			case "invalid schema":
				ddl = strings.Replace(ddl, "summary TEXT", "summary TEXT NOT SQL", 1)
				want = "create rebuilt table"
			case "column mismatch":
				ddl = strings.Replace(ddl, "summary TEXT", "summary TEXT, added TEXT", 1)
				want = "does not match the live one"
			case "copy refused":
				ddl = strings.Replace(ddl, "summary TEXT", "summary TEXT CHECK(summary='refused')", 1)
				want = "copy rows"
			case "broken fts":
				if _, err := db.Exec(`CREATE TABLE journal_entries_fts(wrong_column TEXT)`); err != nil {
					t.Fatal(err)
				}
				want = "rebuild fts index"
			}
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			err = rebuildJournalEntries(context.Background(), conn, ddl, nil)
			if err == nil || !strings.Contains(err.Error(), want) {
				conn.Close()
				t.Fatalf("rebuild=%v, want %s", err, want)
			}
			for _, pragma := range []struct {
				name string
				want int
			}{{"foreign_keys", 1}, {"legacy_alter_table", 0}} {
				var got int
				if err := conn.QueryRowContext(context.Background(), "PRAGMA "+pragma.name).Scan(&got); err != nil || got != pragma.want {
					t.Errorf("%s=%d %v", pragma.name, got, err)
				}
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			var summary, ddlAfter string
			if err := db.QueryRow(`SELECT summary FROM journal_entries WHERE id='entry'`).Scan(&summary); err != nil || summary != "immutable audit" {
				t.Fatalf("audit changed: %q %v", summary, err)
			}
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='journal_entries'`).Scan(&ddlAfter); err != nil || ddlAfter != original {
				t.Fatalf("schema changed: %q %v", ddlAfter, err)
			}
			var indexes, staging int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='audit_summary'`).Scan(&indexes); err != nil || indexes != 1 {
				t.Fatalf("lost index: %d %v", indexes, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, journalRebuildTable).Scan(&staging); err != nil || staging != 0 {
				t.Fatalf("leaked staging: %d %v", staging, err)
			}
		})
	}
}
