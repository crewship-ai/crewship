package database

import (
	"strings"
	"testing"
)

// keeper_runtime_settings is the instance-level judge configuration that makes
// Keeper settable without a restart (see internal/keepercfg for the layering).
//
// Two properties here cannot be recovered later if the migration gets them
// wrong, so the DATABASE enforces them rather than the handler:
//
//  1. it is one row. This is instance configuration, not a collection — a
//     second row would give the resolver two answers and no rule for picking.
//  2. `enabled` is nullable, and NULL is a third state. "The operator has not
//     touched this" must stay distinguishable from "the operator turned it
//     off", or honouring KEEPER_ENABLED is indistinguishable from silently
//     overriding it.

func TestMigrateKeeperRuntimeSettings_TableShape(t *testing.T) {
	db := openMigratedDB(t)

	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='keeper_runtime_settings'`).Scan(&ddl); err != nil {
		t.Fatalf("keeper_runtime_settings table missing after Migrate: %v", err)
	}
	for _, col := range []string{
		"enabled", "judge_provider", "judge_endpoint_url", "judge_wire", "judge_model", "updated_by",
	} {
		if !strings.Contains(ddl, col) {
			t.Errorf("DDL is missing column %q:\n%s", col, ddl)
		}
	}
	// Timestamps must be the T-form the rest of the schema settled on
	// (migrate_v144_datetime_default_tform); a space-form default here would
	// sort before every other timestamp in the database.
	if !strings.Contains(ddl, "strftime('%Y-%m-%dT%H:%M:%fZ','now')") {
		t.Errorf("timestamps must default to the T-form literal:\n%s", ddl)
	}
	// Every judge column inherits from cfg.Keeper when empty, so an instance
	// that has never been configured must be indistinguishable from one that
	// pre-dates the table.
	if strings.Count(ddl, "DEFAULT ''") != 4 {
		t.Errorf("all four judge columns must default to empty (inherit):\n%s", ddl)
	}
}

func TestMigrateKeeperRuntimeSettings_IsASingleton(t *testing.T) {
	db := openMigratedDB(t)

	if _, err := db.Exec(`INSERT INTO keeper_runtime_settings (id) VALUES ('singleton')`); err != nil {
		t.Fatalf("insert the singleton row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO keeper_runtime_settings (id) VALUES ('other')`); err == nil {
		t.Error("the engine accepted a second settings row; the CHECK on id is not doing its job")
	}
}

// NULL means inherit; 0 and 1 are explicit. Anything else is a bug in a caller
// that must not become a stored value the resolver has to interpret.
func TestMigrateKeeperRuntimeSettings_EnabledIsTriState(t *testing.T) {
	db := openMigratedDB(t)

	for _, tc := range []struct {
		name    string
		enabled any
		wantOK  bool
	}{
		{"inherit", nil, true},
		{"explicitly off", 0, true},
		{"explicitly on", 1, true},
		{"nonsense", 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(`DELETE FROM keeper_runtime_settings`); err != nil {
				t.Fatalf("clear: %v", err)
			}
			_, err := db.Exec(
				`INSERT INTO keeper_runtime_settings (id, enabled) VALUES ('singleton', ?)`, tc.enabled)
			if tc.wantOK && err != nil {
				t.Fatalf("enabled=%v rejected: %v", tc.enabled, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("enabled=%v accepted; the CHECK must confine it to 0/1/NULL", tc.enabled)
			}
		})
	}
}
