package database

import "testing"

// TestMigrate_V155_ScheduleCatchupColumns asserts pipeline_schedules gained
// catchup_policy (default 'once') and last_missed_count (default 0) —
// issue #1422 item 2.
func TestMigrate_V155_ScheduleCatchupColumns(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	rows, err := db.Query(`PRAGMA table_info(pipeline_schedules)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	want := map[string]string{"catchup_policy": "", "last_missed_count": ""}
	for rows.Next() {
		var (
			cid   int
			name  string
			ctype string
			nn    int
			d     *string
			pk    int
		)
		if err := rows.Scan(&cid, &name, &ctype, &nn, &d, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if _, ok := want[name]; ok {
			if d != nil {
				want[name] = *d
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	// SQLite echoes the default expression verbatim ('once' with quotes; 0 bare).
	if want["catchup_policy"] != "'once'" {
		t.Errorf("catchup_policy default = %q, want 'once'", want["catchup_policy"])
	}
	if want["last_missed_count"] != "0" {
		t.Errorf("last_missed_count default = %q, want 0", want["last_missed_count"])
	}
}

// TestMigrate_V155_InboxKindWidened asserts inbox_items.kind now admits
// 'schedule_missed' alongside the pre-existing kinds, and that an existing
// row (any kind) survives the CHECK-widening table rebuild.
func TestMigrate_V155_InboxKindWidened(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws_1', 'W', 'w')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('ib_pre', 'ws_1', 'message', 'src_pre', 'pre-existing row')`); err != nil {
		t.Fatalf("seed pre-existing inbox row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('ib_missed', 'ws_1', 'schedule_missed', 'sched_1', 'schedule missed occurrences')`); err != nil {
		t.Fatalf("insert schedule_missed kind should succeed post-v155: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES ('ib_bad', 'ws_1', 'not-a-kind', 'src_bad', 'x')`); err == nil {
		t.Fatal("insert with unknown kind should still fail the CHECK constraint")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE id = 'ib_pre'`).Scan(&count); err != nil {
		t.Fatalf("query pre-existing row: %v", err)
	}
	if count != 1 {
		t.Errorf("pre-existing inbox row lost across the table rebuild")
	}
}
