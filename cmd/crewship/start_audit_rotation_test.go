//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real `crewship start` after a rotate-key crashed between its audit commit
// and the keyring rename: the start's own SQL lookup decides. With the entry
// committed it installs the staged key; without it the keyring is unchanged
// and the staged file is kept aside. Either way the server keeps starting.
func TestStart_SettlesCrashedAuditKeyRotation(t *testing.T) {
	binary := buildCrewshipBinary(t)
	for _, committed := range []bool{true, false} {
		t.Run(fmt.Sprintf("committed=%v", committed), func(t *testing.T) {
			root := t.TempDir()
			master := strings.Repeat("5b", 32)
			if err := os.WriteFile(filepath.Join(root, "secrets.env"), []byte("ENCRYPTION_KEY="+master+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENCRYPTION_KEY", master)
			t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
			keys, keyring, err := journal.EnsureSigningKeys(root, "")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "crewship.db")
			db := testutil.MigratedDBAt(t, path).DB
			_, active, pending, err := journal.StageSigningKeyRotation(root, keys.ActiveID(), "rotationboot1")
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				insertRotationEntry(t, db, keys, "rotationboot1", active)
			}
			_ = db.Close()

			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			storageRoot := filepath.Join(root, "storage")
			configPath := filepath.Join(root, "server.yaml")
			config := fmt.Sprintf("server:\n  host: 127.0.0.1\n  port: %d\nstorage:\n  base_path: %s\n  memory_root: %s\n", port, storageRoot, filepath.Join(root, "memory"))
			if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, binary, "start", "--no-docker", "--config", configPath, "--db", path)
			child.Dir = root
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if strings.HasPrefix(key, "CREWSHIP_") || key == "DATABASE_URL" || key == "NEXTAUTH_SECRET" || strings.HasPrefix(key, "ENCRYPTION_KEY") {
					continue
				}
				child.Env = append(child.Env, entry)
			}
			child.Env = append(child.Env, "CREWSHIP_DATA_DIR="+root, "CREWSHIP_SKIP_SIDECAR=1")
			var output strings.Builder
			child.Stdout, child.Stderr = &output, &output
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan error, 1)
			go func() { exited <- child.Wait() }()
			settled := false
			for !settled {
				select {
				case err := <-exited:
					t.Fatalf("server exited before settling the rotation: %v\n%s", err, output.String())
				case <-ctx.Done():
					t.Fatalf("staged rotation not settled at start:\n%s", output.String())
				case <-time.After(200 * time.Millisecond):
					_, statErr := os.Stat(pending)
					settled = os.IsNotExist(statErr)
				}
			}
			// The server is past the settle point; stop it.
			_ = child.Process.Kill()
			<-exited

			now, err := journal.LoadSigningKeys(keyring)
			if err != nil {
				t.Fatal(err)
			}
			if committed {
				if now.ActiveID() != active {
					t.Fatalf("committed rotation not installed: active %s, want %s", now.ActiveID(), active)
				}
				return
			}
			if now.ActiveID() != keys.ActiveID() {
				t.Fatal("start installed a rotation whose audit entry never committed")
			}
			if _, err := journal.LoadSigningKeys(keyring + ".discarded-rotationboot1"); err != nil {
				t.Fatalf("uncommitted rotation was not kept aside: %v", err)
			}
		})
	}
}

func insertRotationEntry(t *testing.T, db *sql.DB, keys *journal.SigningKeys, id, active string) {
	t.Helper()
	fields := journal.InstanceAuditFields{ID: id, Action: journal.SigningKeyRotatedAction, EntityType: "audit_keyring", EntityID: sql.NullString{String: active, Valid: true}, CreatedAt: "2026-10-23T00:00:00.000000000Z"}
	keyID, version := journal.ActiveSigningIdentity(keys)
	mac, err := journal.InstanceAuditMACForKeys(keys, keyID, version, 1, "", fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO instance_audit_logs(id,action,entity_type,entity_id,created_at,chain_seq,prev_hash,entry_hash,chain_key_id,chain_mac_version) VALUES(?,?,?,?,?,1,'',?,?,?)`,
		fields.ID, fields.Action, fields.EntityType, active, fields.CreatedAt, mac, keyID, version); err != nil {
		t.Fatal(err)
	}
}
