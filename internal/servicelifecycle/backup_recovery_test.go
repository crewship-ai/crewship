package servicelifecycle

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestBackupFenceRecoveryPreservesLiveOwners(t *testing.T) {
	for _, tt := range []struct {
		name, source, target, blocker string
		wantAdopt                     bool
	}{
		{"expired backup retry", "backup", "backup", "", true},
		{"expired backup replaced by restore", "backup", "restore", "", true},
		{"expired restore retry", "restore", "restore", "", true},
		{"live producer", "backup", "backup", "producer", false},
		{"live controller", "backup", "backup", "controller", false},
		{"live service operation", "backup", "backup", "operation", false},
		{"deleted crew", "backup", "restore", "deleted", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, _ := fenceDB(t)
			var oldToken string
			var err error
			if tt.blocker == "deleted" {
				// Recovery can encounter a fence for an already-retired owner.
				if _, err = db.Exec(`UPDATE crews SET deleted_at='deleted'`); err != nil {
					t.Fatal(err)
				}
				oldToken = strings.Repeat("a", 32)
				if _, err = db.Exec(`INSERT INTO service_backup_fences(crew_id,workspace_id,token,created_at,operation,producer_until) VALUES('crew','workspace',?,'',?,'')`, oldToken, tt.source); err != nil {
					t.Fatal(err)
				}
			} else {
				oldToken, err = BeginBackupFence(t.Context(), db, "crew", tt.source)
				if err != nil {
					t.Fatal(err)
				}
			}
			// Emulate a crashed producer. Recovery must retain maintenance until the
			// new owner completes, and may not steal a live writer or producer epoch.
			if tt.blocker != "producer" {
				if _, err = db.Exec(`UPDATE service_backup_fences SET producer_until=''`); err != nil {
					t.Fatal(err)
				}
			}
			live := tsformat.Format(time.Now().Add(time.Minute))
			switch tt.blocker {
			case "controller":
				// Legacy active rows may be present after recovery; writes made before
				// the durable fence remain visible to adoption's lease guard.
				if _, err = db.Exec(`UPDATE service_runtime_intents SET lease_until=?,lease_owner='other'`, live); err != nil {
					t.Fatal(err)
				}
			case "operation":
				if _, err = db.Exec(`INSERT INTO service_operation_leases VALUES('other','crew','workspace',?)`, live); err != nil {
					t.Fatal(err)
				}
			}
			newToken, err := AdoptBackupFence(t.Context(), db, "crew", tt.target)
			if tt.wantAdopt {
				if err != nil || newToken == "" || newToken == oldToken {
					t.Fatalf("adoption token=%q error=%v", newToken, err)
				}
			} else if !errors.Is(err, ErrBackupMaintenance) || newToken != "" {
				t.Fatalf("stole live owner: token=%q error=%v", newToken, err)
			}
			var actual, operation string
			if err = db.QueryRow(`SELECT token,operation FROM service_backup_fences`).Scan(&actual, &operation); err != nil {
				t.Fatal(err)
			}
			if !tt.wantAdopt {
				if actual != oldToken || operation != tt.source {
					t.Fatalf("failed recovery changed ownership: %q %q", actual, operation)
				}
				return
			}
			if actual != newToken || operation != tt.target {
				t.Fatalf("new epoch not persisted: %q %q", actual, operation)
			}
			if err = EndBackupFence(t.Context(), db, "crew", oldToken); !errors.Is(err, ErrBackupMaintenance) {
				t.Fatalf("stale producer released recovery fence: %v", err)
			}
			if err = RenewBackupFence(t.Context(), db, "crew", oldToken); !errors.Is(err, ErrBackupMaintenance) {
				t.Fatalf("stale producer renewed recovery fence: %v", err)
			}
			if err = RenewBackupFence(t.Context(), db, "crew", newToken); err != nil {
				t.Fatal(err)
			}
			if _, err = ServiceOperations(db)(t.Context(), "crew"); !errors.Is(err, ErrBackupMaintenance) {
				t.Fatalf("adoption reopened service admission: %v", err)
			}
			if err = EndBackupFence(t.Context(), db, "crew", newToken); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExpiredBackupProducerCannotRenewOrResumeServices(t *testing.T) {
	db, _ := fenceDB(t)
	token, err := BeginBackupFence(t.Context(), db, "crew", "backup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE service_backup_fences SET producer_until=''`); err != nil {
		t.Fatal(err)
	}
	if err = RenewBackupFence(t.Context(), db, "crew", token); !errors.Is(err, ErrBackupMaintenance) {
		t.Fatalf("expired epoch revived: %v", err)
	}
	if _, err = ServiceOperations(db)(t.Context(), "crew"); !errors.Is(err, ErrBackupMaintenance) {
		t.Fatalf("producer expiry resumed writers: %v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM service_backup_fences`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("maintenance disappeared: %d %v", count, err)
	}
}
