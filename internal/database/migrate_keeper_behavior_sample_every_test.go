package database

import "testing"

// TestMigrate_KeeperBehaviorSampleEveryDefaultsUnset pins the upgrade contract
// for #1001 M3's sampling cadence: the column exists and every pre-existing
// governance row backfills to 0.
//
// 0 is the UNSET sentinel, not "off". It resolves to
// governance.DefaultBehaviorSampleEvery at read time, so a workspace that
// upgrades into this migration keeps sampling exactly as it did — and the
// default stays defined in one place instead of being frozen into old rows.
// A default of 5 here would look equivalent and would not be: it would pin
// every existing workspace to whatever the number happened to be on the day
// they upgraded.
func TestMigrate_KeeperBehaviorSampleEveryDefaultsUnset(t *testing.T) {
	t.Parallel()
	db := migrateChainSetup(t)

	found, def := columnInfo(t, db.DB, "keeper_governance_settings", "behavior_sample_every")
	if !found {
		t.Fatal("keeper_governance_settings missing behavior_sample_every column")
	}
	if def == nil || *def != "0" {
		got := "NULL"
		if def != nil {
			got = *def
		}
		t.Errorf("behavior_sample_every default = %s, want 0 (unset → the built-in cadence)", got)
	}
}
