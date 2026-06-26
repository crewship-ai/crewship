package database

import "testing"

// TestMigrate_V117_ComposioDefaultColumns asserts v117 added the two
// default-connector columns to composio_settings as nullable TEXT. The
// runtime resolver and the /default endpoints SELECT both columns; a missing
// column breaks every agent-config resolve once the flag is armed.
func TestMigrate_V117_ComposioDefaultColumns(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	rows, err := db.Query(`PRAGMA table_info(composio_settings)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	type colInfo struct {
		ctype   string
		notNull int
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
		cols[name] = colInfo{ctype: ctype, notNull: notNull}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	for _, name := range []string{"default_user_id", "default_mcp_server_id"} {
		got, ok := cols[name]
		if !ok {
			t.Errorf("column %s missing from composio_settings after v117", name)
			continue
		}
		if got.ctype != "TEXT" {
			t.Errorf("column %s type = %q, want TEXT", name, got.ctype)
		}
		if got.notNull != 0 {
			t.Errorf("column %s should be nullable (notNull=0), got notNull=%d", name, got.notNull)
		}
	}
}
