//go:build !clionly

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

// Retained metadata from before native alias validation must be inspectable
// without decrypting credentials, rotating them, or attempting a model run.
func TestAcceptance_CredentialUpgradePreflightRetainedData(t *testing.T) {
	t.Setenv("CREWSHIP_CODEX_SANDBOX_MODE", "danger-full-access")
	databasePath := filepath.Join(t.TempDir(), "retained.db")
	db := testutil.MigratedDBAt(t, databasePath).DB
	const token = "crewship_cli_upgrade_preflight_fixture"
	seed := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO workspaces(id,name,slug) VALUES('up-ws','Upgrade','upgrade')`)
	seed(`INSERT INTO users(id,email,full_name) VALUES('up-owner','upgrade@fixture.invalid','Owner')`)
	seed(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('up-member','up-ws','up-owner','OWNER')`)
	seed(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('up-token','up-owner','fixture',?,datetime('now'))`, sha256HexToken(token))
	seed(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('up-crew','up-ws','Upgrade','upgrade')`)
	seed(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,cli_adapter,llm_provider,llm_model) VALUES('up-agent','up-ws','up-crew','Upgrade agent','upgrade-agent','CODEX_CLI','OPENAI','gpt-6.1-sol')`)
	// Intentionally undecryptable: a metadata preflight must not need values.
	for _, c := range []struct{ id, typ, slot string }{{"up-ssh", "SSH_KEY", "NATIVE_KEY"}, {"up-file", "GENERIC_SECRET", "NATIVE_KEY_PATH"}} {
		seed(`INSERT INTO credentials(id,workspace_id,name,type,provider,encrypted_value,status,security_level,created_by) VALUES(?,'up-ws',?,?,'NONE','not-a-decryptable-fixture-value','ACTIVE',3,'up-owner')`, c.id, c.id, c.typ)
		seed(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES(?,?,?,?)`, "grant-"+c.id, "up-agent", c.id, c.slot)
	}
	config := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(config, []byte("format: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(extra ...string) map[string]any {
		t.Helper()
		args := []string{"credential", "upgrade-preflight", "--database", databasePath, "--workspace", "up-ws", "--codex-sandbox-mode", "danger-full-access", "--keeper-enabled=false", "--source-build", "4592d612bcaacd9a0e5dde963dddd6319cb324d5", "-f", "json"}
		cmd := exec.CommandContext(t.Context(), binary, append(args, extra...)...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("native upgrade preflight failed: %v: %s", err, out)
		}
		if strings.Contains(string(out), "not-a-decryptable-fixture-value") || strings.Contains(string(out), token) {
			t.Fatal("preflight leaked credential material")
		}
		var result map[string]any
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("upgrade-preflight did not return JSON: %v: %s", err, out)
		}
		return result
	}
	before := run()
	encoded, _ := json.Marshal(before)
	for _, want := range []string{"up-agent", "up-ssh", "up-file", "NATIVE_KEY_PATH", "danger-full-access"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing upgrade blocker %s: %s", want, encoded)
		}
	}
	if before["ready"] != false {
		t.Fatalf("colliding retained installation reported ready: %s", encoded)
	}
	// Apply the documented metadata repairs without deleting or rotating data.
	seed(`UPDATE agent_credentials SET env_var_name='NATIVE_OTHER_FILE' WHERE credential_id='up-file'`)
	seed(`UPDATE crews SET allow_codex_outer_sandbox=1 WHERE id='up-crew'`)
	if after := run(); after["ready"] != true {
		t.Fatalf("repaired retained metadata not ready: %v", after)
	}
	if unknown := run("--source-build", ""); unknown["ready"] != false || unknown["state"] != "UNKNOWN" {
		t.Fatalf("unverified running configuration passed: %v", unknown)
	}
	// Public resolve hides duplicate exact slots; the retained snapshot must not.
	seed(`UPDATE credentials SET type='GENERIC_SECRET' WHERE id='up-ssh'`)
	seed(`UPDATE agent_credentials SET env_var_name='NATIVE_KEY' WHERE credential_id='up-file'`)
	if duplicate := run(); duplicate["ready"] != false {
		t.Fatalf("duplicate exact slots passed: %v", duplicate)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE encrypted_value='not-a-decryptable-fixture-value'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("preflight changed stored credentials", err)
	}
}
