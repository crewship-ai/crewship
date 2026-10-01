package backup

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

func TestInstanceContentsCheckNeverChangesLiveEncryptionAuthority(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("11", 32))
	t.Setenv(encryption.KeyVersionEnvVar, "v1")
	db := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, db, "contents-key-isolation")
	sealed, err := serviceconfig.Seal(`[{"name":"database","image":"postgres:16","env":{"PASSWORD":"source-private-canary"}}]`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		id := fmt.Sprintf("check-key-%d", i)
		if _, err = db.Exec(`INSERT INTO crews(id,workspace_id,name,slug,services_json) VALUES(?,?,?,?,?)`, id, workspace, id, id, sealed); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	result, err := CreateInstanceBackup(t.Context(), db, InstanceOptions{OutputDir: t.TempDir(), Actor: covAdminActor(), Recipients: []age.Recipient{identity.Recipient()}, RecoveryKit: true})
	if err != nil {
		t.Fatal(err)
	}
	liveHex := strings.Repeat("22", 32)
	t.Setenv("ENCRYPTION_KEY", liveHex)
	liveKey, err := hex.DecodeString(liveHex)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var wrong atomic.Int64
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			envelope, err := encryption.Encrypt("live-private-write")
			if err == nil {
				if _, err = encryption.DecryptWithKeys(envelope, map[string][]byte{"v1": liveKey}); err != nil {
					wrong.Add(1)
				}
			}
		}
	}()
	for i := 0; i < 5; i++ {
		proof, err := CheckBundleContents(t.Context(), result.Path, []age.Identity{identity}, "")
		if err != nil || !proof.OK {
			close(stop)
			<-done
			t.Fatalf("contents check: %+v %v", proof, err)
		}
	}
	close(stop)
	<-done
	if wrong.Load() != 0 {
		t.Fatalf("bundle check changed encryption authority for %d concurrent writes", wrong.Load())
	}
}
