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

func TestMooringPhysicalRecoveryClosesDeliveryWithoutForgettingExpiry(t *testing.T) {
	f := newInstanceFixture(t)
	f.db.SetMaxOpenConns(1)
	if _, e := f.db.Exec(`PRAGMA synchronous=FULL`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('delivery-page','ws_e2e_1','delivery-page','Delivery','u_admin','{}')`); e != nil {
		t.Fatal(e)
	}
	key := mooring.SessionKey{WorkspaceID: "ws_e2e_1", ArtifactID: "delivery-page", ApplicationID: "editor"}
	authority := func(_ context.Context, _ *sql.Tx, a mooring.DeliveryAccess) (string, error) {
		if a.Scope != key {
			return "", mooring.ErrDeliveryClosed
		}
		return "7", nil
	}
	owners := mooring.NewOwnerStore(f.db, true, recoveredOwnerClock{}, time.Minute)
	old, e := owners.Acquire(t.Context(), key, "same-public-runtime-name")
	if e != nil {
		t.Fatal(e)
	}
	delivery := mooring.NewDeliveryStore(owners, true, authority, time.Second, 20)
	g1, e := delivery.Open(t.Context(), old, "human-a", "r1")
	if e != nil {
		t.Fatal(e)
	}
	g2, e := delivery.Open(t.Context(), old, "human-a", "r2")
	if e != nil {
		t.Fatal(e)
	}
	g3, e := delivery.Open(t.Context(), old, "agent-b", "r3")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.BeginTx(t.Context(), nil)
	if e != nil {
		t.Fatal(e)
	}
	barrier, e := delivery.RevokeTx(t.Context(), tx, key, "human-a")
	if e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if e = delivery.AcknowledgeBlocked(t.Context(), old, g1.GateID, barrier.ID); e != nil {
		t.Fatal(e)
	}
	f.db.SetMaxOpenConns(10) // instance collection performs nested queries
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
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(`PRAGMA synchronous=FULL`); e != nil {
		t.Fatal(e)
	}
	var total, closed, blocked int
	if e = db.QueryRow(`SELECT count(*),sum(closed),sum(blocked) FROM mooring_delivery_gates WHERE page_id='delivery-page'`).Scan(&total, &closed, &blocked); e != nil || total != 3 || closed != 3 || blocked != 0 {
		t.Fatal("source capabilities revived or expiry records omitted", total, closed, blocked, e)
	}
	targetOwners := mooring.NewOwnerStore(db.DB, true, recoveredOwnerClock{}, time.Minute)
	target := mooring.NewDeliveryStore(targetOwners, true, authority, time.Second, 20)
	status, e := target.Barrier(t.Context(), key, "human-a", barrier.ID)
	if e != nil || status.Complete || status.RemainingMilliseconds != 1000 {
		t.Fatal("same-boot source expiry forgotten", status, e)
	}
	fresh, e := targetOwners.Acquire(t.Context(), key, old.OwnerID)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.Generation != old.Generation || fresh.Token == old.Token {
		t.Fatal("fixture failed to reuse public identity with fresh private capability")
	}
	if e = target.AcknowledgeBlocked(t.Context(), fresh, g2.GateID, barrier.ID); !errors.Is(e, mooring.ErrDeliveryClosed) {
		t.Fatal("new runtime acknowledged source paths", e)
	}
	if _, e = target.Renew(t.Context(), fresh, g3.GateID, "replay-active-source"); !errors.Is(e, mooring.ErrDeliveryClosed) {
		t.Fatal("source active gate resumed", e)
	}
	if e = f.db.QueryRow(`SELECT count(*),sum(closed),sum(blocked) FROM mooring_delivery_gates WHERE page_id='delivery-page'`).Scan(&total, &closed, &blocked); e != nil || total != 3 || closed != 2 || blocked != 1 {
		t.Fatal("recovery changed source state", total, closed, blocked, e)
	}
}
