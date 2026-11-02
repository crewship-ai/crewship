package database

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const compactStorageTriggersMigration = "artifact_storage_compact_triggers"

// storageTriggerTemplate matches the INSERT trigger body that
// 20261030224950_artifact_aggregate_storage_budget installed for every
// accounted table: (workspace expression, size expression).
var storageTriggerTemplate = regexp.MustCompile(`(?s)^CREATE TRIGGER (\w+)_storage_insert AFTER INSERT ON (\w+)\nBEGIN\n` +
	` INSERT INTO artifact_storage_workspaces\(workspace_id,max_bytes\) SELECT (.*),workspace_max_bytes FROM artifact_storage_global WHERE id=1 AND (.*) IS NOT NULL ON CONFLICT\(workspace_id\) DO NOTHING;\n` +
	` UPDATE artifact_storage_workspaces SET used_bytes=used_bytes\+\((.*)\) WHERE workspace_id=(.*);\n` +
	` UPDATE artifact_storage_global SET used_bytes=used_bytes\+\((.*)\) WHERE id=1;\nEND$`)

func openStorageTriggerDB(t *testing.T, before bool) *sql.DB {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := Open(filepath.Join(t.TempDir(), "storage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if before {
		err = MigrateBefore(context.Background(), db.DB, logger, compactStorageTriggersMigration)
	} else {
		err = Migrate(context.Background(), db.DB, logger)
	}
	if err != nil {
		t.Fatal(err)
	}
	return db.DB
}

func storageTriggerSQL(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type='trigger' AND (name LIKE '%\_storage\_insert' ESCAPE '\' OR name LIKE '%\_storage\_update' ESCAPE '\' OR name LIKE '%\_storage\_delete' ESCAPE '\')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			t.Fatal(err)
		}
		out[name] = body
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every accounted table keeps its workspace and size rule exactly: each
// compact trigger hands the expressions the #4647 trigger used to the shared
// artifact_storage_charge procedure, and nothing else. The "after" side is the
// head schema: a later migration that changes one of these triggers must
// update this test (and keep the compact one-statement shape).
func TestCompactStorageTriggersKeepEveryTablesWorkspaceAndSizeRule(t *testing.T) {
	before := storageTriggerSQL(t, openStorageTriggerDB(t, true))
	after := storageTriggerSQL(t, openStorageTriggerDB(t, false))
	tables := 0
	for name, body := range before {
		if !strings.HasSuffix(name, "_storage_insert") || strings.HasPrefix(name, "pages_") {
			continue
		}
		m := storageTriggerTemplate.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("%s no longer matches the #4647 template; regenerate the compact triggers:\n%s", name, body)
		}
		table, wn, sn := m[2], m[3], m[5]
		if m[1] != table || m[4] != wn || m[6] != wn || m[7] != sn {
			t.Fatalf("%s: inconsistent template instance", name)
		}
		wo, so := strings.ReplaceAll(wn, "NEW.", "OLD."), strings.ReplaceAll(sn, "NEW.", "OLD.")
		want := map[string]string{
			table + "_storage_insert": fmt.Sprintf("CREATE TRIGGER %s_storage_insert AFTER INSERT ON %s BEGIN INSERT INTO artifact_storage_charge VALUES('i',NULL,%s,0,%s); END", table, table, wn, sn),
			table + "_storage_delete": fmt.Sprintf("CREATE TRIGGER %s_storage_delete AFTER DELETE ON %s BEGIN INSERT INTO artifact_storage_charge VALUES('d',%s,NULL,%s,0); END", table, table, wo, so),
			table + "_storage_update": fmt.Sprintf("CREATE TRIGGER %s_storage_update AFTER UPDATE ON %s BEGIN INSERT INTO artifact_storage_charge VALUES('u',%s,%s,%s,%s); END", table, table, wo, wn, so, sn),
		}
		for trigger, sql := range want {
			if after[trigger] != sql {
				t.Errorf("%s:\n got %s\nwant %s", trigger, after[trigger], sql)
			}
		}
		tables++
	}
	if tables != 30 {
		t.Fatalf("checked %d accounted tables, want the 30 from #4647", tables)
	}
}

// The per-connection schema parse is what made every fixture slower (#4764).
// Keep the accounting triggers' text bounded so it cannot creep back.
func TestCompactStorageTriggersStayWithinSchemaBudget(t *testing.T) {
	total := 0
	for _, body := range storageTriggerSQL(t, openStorageTriggerDB(t, false)) {
		total += len(body)
	}
	if total > 48*1024 {
		t.Fatalf("storage accounting triggers are %d bytes of schema, budget 48 KiB (was ~110 KiB before #4764)", total)
	}
}

type storageStep struct {
	name string
	sql  string
}

func storageCounters(t *testing.T, conn *sql.Conn) string {
	t.Helper()
	rows, err := conn.QueryContext(context.Background(), `SELECT workspace_id||'='||used_bytes FROM artifact_storage_workspaces UNION ALL SELECT 'global='||used_bytes FROM artifact_storage_global ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func runStorageSteps(t *testing.T, db *sql.DB, steps []storageStep) []string {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Accounting only: parent rows are irrelevant to the counters under test.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	var trace []string
	for _, step := range steps {
		res := "ok"
		if _, err := conn.ExecContext(ctx, step.sql); err != nil {
			res = "err:" + err.Error()
		}
		trace = append(trace, fmt.Sprintf("%s -> %s | %s", step.name, res, storageCounters(t, conn)))
	}
	return trace
}

// Behavioural equivalence on a direct-workspace table and a Page-derived one:
// growth, shrink, workspace moves, clamping after drift, unscoped rows and
// quota refusal produce identical counters before and after compaction.
func TestCompactStorageTriggersAccountExactlyLikeTheOriginals(t *testing.T) {
	steps := []storageStep{
		{"pages", `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('p1','w1','p1','P1','o','{}'),('p2','w2','p2','P2','o','{}')`},
		{"insert w1", `INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('w1','p1','h1',zeroblob(300))`},
		{"grow same ws", `UPDATE artifact_version_blobs SET content=zeroblob(500) WHERE hash='h1'`},
		{"no-op update", `UPDATE artifact_version_blobs SET content=content WHERE hash='h1'`},
		{"move to w2", `UPDATE artifact_version_blobs SET workspace_id='w2',page_id='p2' WHERE hash='h1'`},
		{"drift w2 low", `UPDATE artifact_storage_workspaces SET used_bytes=10 WHERE workspace_id='w2'`},
		{"shrink clamps", `UPDATE artifact_version_blobs SET content=zeroblob(100) WHERE hash='h1'`},
		{"second row w2", `INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('w2','p2','h2',zeroblob(40))`},
		{"delete clamps", `DELETE FROM artifact_version_blobs WHERE hash='h1'`},
		{"drift global low", `UPDATE artifact_storage_global SET used_bytes=5 WHERE id=1`},
		{"delete global clamps", `DELETE FROM artifact_version_blobs WHERE hash='h2'`},
		{"tight w1 quota", `UPDATE artifact_storage_workspaces SET max_bytes=1000 WHERE workspace_id='w1'`},
		{"insert over quota", `INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('w1','p1','h3',zeroblob(2000))`},
		{"insert under quota", `INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES('w1','p1','h3',zeroblob(100))`},
		{"grow over quota", `UPDATE artifact_version_blobs SET content=zeroblob(5000) WHERE hash='h3'`},
		{"move over quota", `UPDATE artifact_storage_workspaces SET max_bytes=150 WHERE workspace_id='w2'`},
		{"move refused", `UPDATE artifact_version_blobs SET workspace_id='w2',page_id='p2' WHERE hash='h3'`},
		{"page scope", `INSERT INTO artifact_storage_pages(page_id,workspace_id) VALUES('gp','w3')`},
		{"derived insert", `INSERT INTO gridcraft_revisions(page_id,rev,snapshot) VALUES('gp',1,zeroblob(200))`},
		{"derived grow", `UPDATE gridcraft_revisions SET snapshot=zeroblob(260) WHERE page_id='gp'`},
		{"rescope page", `UPDATE artifact_storage_pages SET workspace_id='w4' WHERE page_id='gp'`},
		{"derived update after rescope", `UPDATE gridcraft_revisions SET snapshot=zeroblob(270) WHERE page_id='gp'`},
		{"unscoped insert", `INSERT INTO gridcraft_revisions(page_id,rev,snapshot) VALUES('nowhere',1,zeroblob(70))`},
		{"unscoped update", `UPDATE gridcraft_revisions SET snapshot=zeroblob(90) WHERE page_id='nowhere'`},
		{"unscoped delete", `DELETE FROM gridcraft_revisions WHERE page_id='nowhere'`},
		{"derived delete", `DELETE FROM gridcraft_revisions WHERE page_id='gp'`},
	}
	want := runStorageSteps(t, openStorageTriggerDB(t, true), steps)
	got := runStorageSteps(t, openStorageTriggerDB(t, false), steps)
	refusals := 0
	for i := range steps {
		if got[i] != want[i] {
			t.Errorf("step %d diverged:\n got %s\nwant %s", i, got[i], want[i])
		}
		if strings.Contains(want[i], "artifact_storage_quota") {
			refusals++
		}
	}
	if refusals != 3 {
		t.Fatalf("fixture exercised %d quota refusals, want 3:\n%s", refusals, strings.Join(want, "\n"))
	}
}
