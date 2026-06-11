package database

import "testing"

// TestMigrate_V115_WakeGateColumns asserts v115 added the six wake-gate
// columns to pipeline_schedules with the documented NULL/NOT NULL +
// default shape. The schedule store's SELECT scans all six on every
// tick, so a missing column or a NULL where the scanner expects a
// value breaks the scheduler at runtime.
func TestMigrate_V115_WakeGateColumns(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	rows, err := db.Query(`PRAGMA table_info(pipeline_schedules)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	type colInfo struct {
		notNull int
		dflt    string
	}
	cols := map[string]colInfo{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    *string
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		d := ""
		if dflt != nil {
			d = *dflt
		}
		cols[name] = colInfo{notNull: notNull, dflt: d}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	want := map[string]colInfo{
		"wake_pipeline_id": {notNull: 0, dflt: ""},
		"wake_inputs_json": {notNull: 1, dflt: "'{}'"},
		"wake_check_count": {notNull: 1, dflt: "0"},
		"wake_fire_count":  {notNull: 1, dflt: "0"},
		"last_wake_at":     {notNull: 0, dflt: ""},
		"last_wake_status": {notNull: 0, dflt: ""},
	}
	for name, w := range want {
		got, ok := cols[name]
		if !ok {
			t.Errorf("column %s missing from pipeline_schedules after v115", name)
			continue
		}
		if got != w {
			t.Errorf("column %s: got %+v, want %+v", name, got, w)
		}
	}
}
