package backup_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// The clock deliberately stays in the same boot and before expiry so elapsed
// time cannot accidentally make a resurrected capability look safely fenced.
type recoveredOwnerClock struct{}

func (recoveredOwnerClock) Now() (string, time.Duration, error) { return "same-boot", time.Second, nil }
func TestMooringPhysicalRecoveryCannotResurrectSourceOwner(t *testing.T) {
	f := newInstanceFixture(t)
	f.db.SetMaxOpenConns(1)
	if _, e := f.db.Exec(`PRAGMA synchronous=FULL`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('owner-page','ws_e2e_1','owner-page','Owner','u_admin','{}')`); e != nil {
		t.Fatal(e)
	}
	source := mooring.NewOwnerStore(f.db, true, recoveredOwnerClock{}, time.Minute)
	lease, e := source.Acquire(t.Context(), mooring.SessionKey{WorkspaceID: "ws_e2e_1", ArtifactID: "owner-page", ApplicationID: "editor"}, "source-runtime")
	if e != nil {
		t.Fatal(e)
	}
	// Backup collection uses nested queries; restore the fixture pool after
	// the single-connection synchronous=FULL owner acquisition.
	f.db.SetMaxOpenConns(10)
	created := f.create(t, true)
	targetDir := filepath.Join(t.TempDir(), "recovered")
	report, e := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: targetDir, Actor: "u_admin"})
	if e != nil || report.Result != backup.RestoreResultOK {
		t.Fatalf("physical recovery: %#v %v", report, e)
	}
	db, e := database.Open("file:" + filepath.Join(targetDir, "crewship.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	target := mooring.NewOwnerStore(db.DB, true, recoveredOwnerClock{}, time.Minute)
	tx, e := db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = target.Check(t.Context(), tx, lease); !errors.Is(e, mooring.ErrOwnerStale) {
		t.Fatalf("source ownership resurrected: %v", e)
	}
	var count int
	if e = f.db.QueryRow(`SELECT count(*) FROM mooring_session_owners WHERE page_id='owner-page'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("recovery changed source ownership: %d %v", count, e)
	}
}
