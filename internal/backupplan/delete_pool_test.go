package backupplan

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Hold every other pool slot without issuing SQL: the only SQL connection
// available to deletion is its caller-owned transaction, with no lock contention.
func saturatedDeleteTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	const poolSize = 3
	db.SetMaxOpenConns(poolSize)
	db.SetMaxIdleConns(poolSize)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	for i := 0; i < poolSize-1; i++ {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if stats := db.Stats(); stats.InUse != poolSize || stats.OpenConnections != poolSize {
		t.Fatalf("pool is not saturated: %+v", stats)
	}
	return tx
}

func TestDeleteRecipientSaturatedPool(t *testing.T) {
	for _, reference := range []string{"id", "public key", "unused"} {
		t.Run(reference, func(t *testing.T) {
			db := testutil.MigratedDBAt(t, filepath.Join(t.TempDir(), "backup.db")).DB
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			identity, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			r := Recipient{Name: "Recovery key", PublicKey: identity.Recipient().String()}
			now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
			if err := InsertRecipient(ctx, db, &r, "", now); err != nil {
				t.Fatal(err)
			}
			p := Defaults(backup.PresetComplete)
			p.Name = "Nightly recovery"
			if reference == "id" {
				p.RecipientIDs = []string{r.ID}
			}
			if reference == "public key" {
				p.RecipientIDs = []string{r.PublicKey}
			}
			if err := InsertPlan(ctx, db, &p, "", now); err != nil {
				t.Fatal(err)
			}
			tx := saturatedDeleteTx(t, db)
			callCtx, callCancel := context.WithTimeout(ctx, time.Second)
			used, deleteErr := DeleteRecipient(callCtx, db, tx, r.ID)
			callCancel()
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_recipients WHERE id=?`, r.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if reference == "unused" {
				if deleteErr != nil || len(used) != 0 || count != 0 {
					t.Errorf("unused deletion: used=%v err=%v remaining=%d", used, deleteErr, count)
				}
			} else if !errors.Is(deleteErr, ErrRecipientInUse) || !reflect.DeepEqual(used, []string{p.Name}) || count != 1 {
				t.Errorf("named refusal must preserve key: used=%v err=%v remaining=%d", used, deleteErr, count)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeleteDestinationSaturatedPool(t *testing.T) {
	for _, inUse := range []bool{true, false} {
		name := "unused"
		if inUse {
			name = "in use"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.MigratedDBAt(t, filepath.Join(t.TempDir(), "backup.db")).DB
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
			d := destinationFixture()
			d.Name = "Recovery storage"
			if err := InsertDestination(ctx, db, &d, "fixture-envelope", "", now); err != nil {
				t.Fatal(err)
			}
			if err := RecordCopy(ctx, db, Copy{BundlePath: "fixture.tar.zst", DestinationID: d.ID, Key: "fixture", Size: 42, SHA256: "fixture", VerifiedAt: ts(now)}); err != nil {
				t.Fatal(err)
			}
			p := Defaults(backup.PresetComplete)
			p.Name = "Nightly recovery"
			if inUse {
				p.Destinations = []string{DestinationLocal, d.ID}
			}
			if err := InsertPlan(ctx, db, &p, "", now); err != nil {
				t.Fatal(err)
			}
			tx := saturatedDeleteTx(t, db)
			callCtx, callCancel := context.WithTimeout(ctx, time.Second)
			used, deleteErr := DeleteDestination(callCtx, db, tx, d.ID)
			callCancel()
			var destinations, copies int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_offsite_destinations WHERE id=?`, d.ID).Scan(&destinations); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_copies WHERE destination_id=?`, d.ID).Scan(&copies); err != nil {
				t.Fatal(err)
			}
			if inUse {
				if !errors.Is(deleteErr, ErrDestinationInUse) || !reflect.DeepEqual(used, []string{p.Name}) || destinations != 1 || copies != 1 {
					t.Errorf("named refusal must preserve destination and copy: used=%v err=%v destinations=%d copies=%d", used, deleteErr, destinations, copies)
				}
			} else if deleteErr != nil || len(used) != 0 || destinations != 0 || copies != 0 {
				t.Errorf("unused deletion: used=%v err=%v destinations=%d copies=%d", used, deleteErr, destinations, copies)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
