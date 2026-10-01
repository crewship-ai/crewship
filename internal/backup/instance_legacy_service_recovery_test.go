package backup

import (
	"strings"
	"testing"

	"filippo.io/age"
)

func TestLegacyInstanceWithoutServiceFenceTablesRecoversWithoutVaultKey(t *testing.T) {
	db := openMigratedDBCov(t)
	seedCovWorkspace(t, db, "legacy-service-schema")
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='trigger' AND sql LIKE '%service_backup_fences%'`)
	if err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, name)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range triggers {
		if _, err = db.Exec(`DROP TRIGGER "` + strings.ReplaceAll(name, `"`, `""`) + `"`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`DROP TABLE service_backup_fences; DROP TABLE service_operation_leases; DELETE FROM _migrations WHERE version>=20261001160000`); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	result, err := CreateInstanceBackup(t.Context(), db, InstanceOptions{OutputDir: t.TempDir(), Actor: covAdminActor(), Recipients: []age.Recipient{identity.Recipient()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTION_KEY", "")
	report, err := RecoverInstance(t.Context(), RecoverOptions{BundlePath: result.Path, Identities: []age.Identity{identity}, DataDir: t.TempDir(), Drill: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) == 0 {
		t.Fatal("missing deferred migration warning")
	}
}
