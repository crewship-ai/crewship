package database

import "testing"

// TestMigrate_V151_WakeFailClosedColumn asserts v151 added the opt-in
// fail-closed policy column to pipeline_schedules as NOT NULL DEFAULT 0,
// so existing schedules keep the fail-open default and the schedule
// store's SELECT can scan it on every tick without a NULL surprise
// (#1372).
func TestMigrate_V151_WakeFailClosedColumn(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	rows, err := db.Query(`PRAGMA table_info(pipeline_schedules)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	var (
		found   bool
		notNull int
		dflt    string
	)
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
		if name == "wake_fail_closed" {
			found = true
			notNull = nn
			if d != nil {
				dflt = *d
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if !found {
		t.Fatal("column wake_fail_closed missing from pipeline_schedules after v151")
	}
	if notNull != 1 || dflt != "0" {
		t.Errorf("wake_fail_closed: got notNull=%d dflt=%q, want notNull=1 dflt=\"0\"", notNull, dflt)
	}
}
