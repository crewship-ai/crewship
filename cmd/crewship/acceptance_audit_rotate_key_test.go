package main

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: the instance OWNER rotates the
// audit signing key; the keyring gains a new active key and keeps the old.
func TestAcceptance_AuditRotateKey(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	root := t.TempDir()
	t.Setenv("CREWSHIP_HOME", root)
	t.Setenv("CREWSHIP_DATA_DIR", "")
	keys, path, err := journal.EnsureSigningKeys(root, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(journal.SigningKeysPathEnv, path)
	t.Setenv(backup.InstanceOwnerEmailEnv, "owner@keys.invalid")
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_auditrotatekey000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('rk-ws','Keys','keys','2026-01-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('rk-owner','owner@keys.invalid','Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('rk-m1','rk-ws','rk-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('rk-token','rk-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: keys\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("audit", "rotate-key", "--reason", "short"); err == nil {
		t.Fatalf("a short reason was accepted:\n%s", out)
	}
	if out, err := run("audit", "rotate-key", "--reason", "Scheduled key rotation", "--expected-active-key-id", "key-stale"); err == nil || !strings.Contains(out, "active audit key changed") {
		t.Fatalf("stale expected key: %v\n%s", err, out)
	}
	out, err := run("audit", "rotate-key", "--reason", "Scheduled key rotation", "--expected-active-key-id", keys.ActiveID())
	if err != nil {
		t.Fatalf("rotate-key: %v\n%s", err, out)
	}
	rotated, err := journal.LoadSigningKeys(path)
	if err != nil || rotated.ActiveID() == keys.ActiveID() || !strings.Contains(out, rotated.ActiveID()) || !strings.Contains(out, "Restart the server") {
		t.Fatalf("rotation not applied or not reported: %v\n%s", err, out)
	}
	verified, err := journal.VerifyInstanceAuditWithKeys(t.Context(), db, rotated)
	if err != nil || !verified.OK || verified.Count != 1 {
		t.Fatalf("rotation audit entry does not verify with the rotated keyring: %+v %v", verified, err)
	}
}
