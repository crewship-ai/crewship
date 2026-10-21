package api

import (
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logging"
)

func TestAuditAutoRevertRetainsBootSigningSnapshot(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-auto-revert-legacy")
	old, path, err := journal.EnsureSigningKeys(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(journal.SigningKeysPathEnv, path)
	db := setupTestDB(t)
	logger := logging.New("info", "json", nil)
	defer logging.ResetLevel()
	defer logging.OnAutoRevert(nil)
	NewAdminObservabilityHandler(db, logger)
	newer, _, err := journal.EnsureSigningKeys(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := journal.ExportSigningKeys(newer)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	// Only the synthetic fixture file changes. A timed callback must retain the
	// boot snapshot rather than adopting an externally replaced file mid-boot.
	if _, err := logging.SetLevel("debug", 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action='instance.log_level_reverted'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("native timed revert did not append its audit")
		}
	}
	var id string
	var version int
	if err := db.QueryRow(`SELECT chain_key_id,chain_mac_version FROM instance_audit_logs WHERE action='instance.log_level_reverted'`).Scan(&id, &version); err != nil {
		t.Fatal(err)
	}
	if id != old.ActiveID() || version != 2 {
		t.Error("timed callback adopted a changed external keyfile instead of its boot snapshot")
	}
}
