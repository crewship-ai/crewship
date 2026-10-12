package database

import (
	"testing"
	"testing/fstest"
)

func TestMigrationFingerprintTracksInputs(t *testing.T) {
	inputs := fstest.MapFS{
		"migrate.go":                            {Data: []byte("Go migration implementation")},
		"migrations/20261006170000_fixture.sql": {Data: []byte("CREATE TABLE fixture(id TEXT);")},
		"migrate_test.go":                       {Data: []byte("test")},
	}
	fingerprint := func() string {
		t.Helper()
		key, err := migrationFingerprint(inputs)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	original := fingerprint()
	inputs["migrate_test.go"].Data = []byte("changed test")
	if fingerprint() != original {
		t.Fatal("test-only edit invalidated fixtures")
	}
	for _, name := range []string{"migrate.go", "migrations/20261006170000_fixture.sql"} {
		t.Run(name, func(t *testing.T) {
			before := fingerprint()
			inputs[name].Data = append(inputs[name].Data, []byte("changed migration")...)
			if fingerprint() == before {
				t.Fatal("migration content edit reused old fingerprint")
			}
		})
	}
	before := fingerprint()
	inputs["migrations/20261006180000_new.sql"] = &fstest.MapFile{Data: []byte("SELECT 1;")}
	if fingerprint() == before {
		t.Fatal("new migration reused old fingerprint")
	}
}
