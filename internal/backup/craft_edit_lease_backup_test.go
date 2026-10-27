package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func editLeaseRecoveryStore(db *sql.DB) *mooring.EditLeaseStore {
	return mooring.NewEditLeaseStore(db, mooring.EditLeaseConfig{Enabled: true, Clock: recoveredOwnerClock{}, TTL: time.Minute, Authority: func(context.Context, *sql.Tx, mooring.EditLeaseAccess) error { return nil }})
}
func editLeaseRecoveryFixture(t *testing.T) (*instanceFixture, *mooring.EditLeaseStore, mooring.EditLeaseScope, mooring.EditLeasePrincipal, mooring.EditLease) {
	t.Helper()
	f := newInstanceFixture(t)
	var seq int
	var name, path string
	if err := f.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	durable, err := database.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { durable.Close() })
	f.db = durable.DB
	if _, err := f.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('lease-page','ws_e2e_1','lease-page','Lease','u_admin','{}')`); err != nil {
		t.Fatal(err)
	}
	source := editLeaseRecoveryStore(f.db)
	scope := mooring.EditLeaseScope{WorkspaceID: "ws_e2e_1", ArtifactID: "lease-page"}
	principal := mooring.EditLeasePrincipal{ActorID: "u_admin", SessionID: "original-tab"}
	lease, err := source.Acquire(t.Context(), scope, principal)
	if err != nil {
		t.Fatal(err)
	}
	if err = source.RequestHandoff(t.Context(), scope, mooring.EditLeasePrincipal{ActorID: "u_admin", SessionID: "other-tab"}); err != nil {
		t.Fatal(err)
	}
	return f, source, scope, principal, lease
}
func TestCraftEditLeaseBackupExcludesCapabilitiesAndProtectsTarget(t *testing.T) {
	f, source, scope, _, lease := editLeaseRecoveryFixture(t)
	dump, err := backup.DumpWorkspace(t.Context(), f.db, scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mooring_edit_leases", "mooring_edit_lease_requests"} {
		if len(dump.Tables[table]) != 0 {
			t.Fatalf("portable backup leaked runtime authority: %s", table)
		}
	}
	// Even a released target retains its issued generation. Replacing its Page
	// would cascade-delete that tombstone and allow a delayed generation to recur.
	if err = source.Release(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if err = backup.RestoreDump(t.Context(), f.db, dump); !errors.Is(err, backup.ErrMooringDeliveryTargetReplacement) {
		t.Fatalf("target generation replacement admitted: %v", err)
	}

	var generation int64
	if err = f.db.QueryRow(`SELECT generation FROM mooring_edit_leases WHERE page_id=?`, scope.ArtifactID).Scan(&generation); err != nil || generation != lease.Generation {
		t.Fatalf("target tombstone changed: %d %v", generation, err)
	}
}
func TestCraftEditLeaseBackupFencesSourceAndPreservesGeneration(t *testing.T) {
	f, source, scope, principal, lease := editLeaseRecoveryFixture(t)
	created := f.create(t, true)
	targetDir := filepath.Join(t.TempDir(), "recovered")
	report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: targetDir, Actor: "u_admin"})
	if err != nil || report.Result != backup.RestoreResultOK {
		t.Fatalf("recovery: %#v %v", report, err)
	}
	db, err := database.Open("file:" + filepath.Join(targetDir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target := editLeaseRecoveryStore(db.DB)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = target.CheckWrite(t.Context(), tx, lease)
	tx.Rollback()
	if !errors.Is(err, mooring.ErrEditLeaseStale) {
		t.Fatalf("same-boot source authority survived recovery: %v", err)
	}
	var actor, session string
	var generation, expires int64
	var requests int
	if err = db.QueryRow(`SELECT actor_id,session_id,generation,expires_ns FROM mooring_edit_leases WHERE page_id='lease-page'`).Scan(&actor, &session, &generation, &expires); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM mooring_edit_lease_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if actor != "" || session != "" || generation != lease.Generation || expires != 0 || requests != 0 {
		t.Fatalf("unsafe recovered lease: %q %q %d %d requests=%d", actor, session, generation, expires, requests)
	}
	fresh, err := target.Acquire(t.Context(), scope, principal)
	if err != nil || fresh.Generation != lease.Generation+1 {
		t.Fatalf("generation reused: %#v %v", fresh, err)
	}
	if _, err = source.Renew(t.Context(), lease); err != nil {
		t.Fatalf("recovery mutated source: %v", err)
	}
}
