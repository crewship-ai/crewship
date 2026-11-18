package orchestrator_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/testutil"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// No stub credential API or resolver: the built CLI stores and assigns against
// real migrated SQLite, IPC decrypts through the production resolver, and the
// production native writer/env feed standard OpenSSH against a local SSH fixture.
func TestNativeVaultCLIToStandardSSH(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	const workspace = "cnativews00000000000001"
	const agent = "cnativeagent00000000001"
	const peer = "cnativepeer000000000001"
	const token = "crewship_cli_synthetic_native_fixture_0000000000"
	const master = "synthetic-native-internal-token"
	sum := sha256.Sum256([]byte(token))
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Native fixture','native-fixture')`, workspace)
	seed(`INSERT INTO users(id,email,full_name) VALUES('native-owner','owner@native.invalid','Fixture')`)
	seed(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-member',?,'native-owner','OWNER')`, workspace)
	seed(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('native-token','native-owner','fixture',?,datetime('now'))`, hex.EncodeToString(sum[:]))
	seed(`INSERT INTO crews(id,workspace_id,name,slug,network_mode,container_memory_mb,container_cpus,created_at) VALUES('native-crew',?,'Native','native','free',512,1,datetime('now'))`, workspace)
	for _, id := range []string{agent, peer} {
		seed(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,llm_provider,llm_model,tool_profile,timeout_seconds,memory_enabled,created_at) VALUES(?,?,'native-crew',?,?,'AGENT','IDLE','CLAUDE_CODE','ANTHROPIC','claude-sonnet-5-5','CODING',60,0,datetime('now'))`, id, workspace, id, id)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger, api.WithInternalToken(master))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	defer server.Close()
	dir := t.TempDir()
	binary := filepath.Join(dir, "crewship")
	build := exec.Command("go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture CLI: %v %s", err, out)
	}
	config := filepath.Join(dir, "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cli := func(input string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, args...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		if err != nil {
			if failure, ok := err.(*exec.ExitError); ok {
				t.Fatalf("fixture CLI %v: %v %s", args, err, failure.Stderr)
			}
			t.Fatal(err)
		}
		if input != "" && strings.Contains(string(out), input) {
			t.Fatal("credential value leaked on CLI stdout")
		}
		return out
	}
	makeKey := func() (string, ssh.Signer) {
		t.Helper()
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ssh.MarshalPrivateKey(key, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return string(pem.EncodeToMemory(block)), signer
	}
	canonical, signer := makeKey()
	replacement, newSigner := makeKey()
	original := strings.ReplaceAll(canonical, "\n", "\r\n")
	input := filepath.Join(dir, "synthetic-key")
	if err := os.WriteFile(input, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	out := cli("", "credential", "create", "--name", "NATIVE_SSH", "--type", "SSH_KEY", "--provider", "NONE", "--env-var-name", "NATIVE_SSH", "--from-file", input)
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.ID == "" {
		t.Fatal("CLI did not return credential id", err)
	}
	var encrypted string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id=?`, created.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == original || strings.Contains(encrypted, "PRIVATE KEY") {
		t.Fatal("Vault did not encrypt private key")
	}
	// The same safe CLI field path used by certificate key_pem must preserve
	// a whole key, not just the first scanner line, through encrypted storage.
	cli(canonical, "credential", "field", "set", created.ID, "key_pem", "--value-stdin")
	var fieldPlain, fieldEncrypted string
	if err := db.QueryRow(`SELECT COALESCE(value,''),encrypted_value FROM credential_fields WHERE credential_id=? AND key='key_pem'`, created.ID).Scan(&fieldPlain, &fieldEncrypted); err != nil {
		t.Fatal(err)
	}
	if fieldPlain != "" || fieldEncrypted == "" || fieldEncrypted == canonical {
		t.Fatal("secret field was not encrypted at rest")
	}
	resolver := chatbridge.NewIPCResolver(server.URL, master, logger)
	resolve := func(id string) []orchestrator.Credential {
		t.Helper()
		info, err := resolver.ResolveAgent(context.Background(), id, workspace)
		if err != nil {
			t.Fatal(err)
		}
		return info.Credentials
	}
	if len(resolve(agent)) != 0 {
		t.Fatal("unassigned key delivered")
	}
	cli("", "credential", "assign", created.ID, agent, "--env-var-name", "NATIVE_SSH", "--ttl", "30m")
	creds := resolve(agent)
	if len(creds) != 1 || creds[0].ID != created.ID || creds[0].EnvVarName != "NATIVE_SSH" || creds[0].PlainValue != original {
		t.Fatal("CLI/Vault/resolver changed bytes or slot")
	}
	if len(creds[0].Fields) != 1 || creds[0].Fields[0].Value != canonical || creds[0].Fields[0].EnvVar != "NATIVE_SSH_KEY_PEM" || !creds[0].Fields[0].IsSecret {
		t.Fatal("CLI/Vault/resolver changed the secret field bytes or slot")
	}
	if len(resolve(peer)) != 0 {
		t.Fatal("ungranted peer received credential through resolver")
	}
	port := orchestrator.NativeSSHFixtureForTest(t, signer, newSigner.PublicKey())
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{fmt.Sprintf("[127.0.0.1]:%d", port)}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	authenticate := func(creds []orchestrator.Credential) string {
		t.Helper()
		files := t.TempDir()
		script, _, _, err := orchestrator.NativeCredentialScriptForTest(creds, files, true)
		if err != nil {
			t.Fatal(err)
		}
		writer := exec.Command("sh")
		writer.Stdin = strings.NewReader(script)
		if err := writer.Run(); err != nil {
			t.Fatal(err)
		}
		req := orchestrator.AgentRunRequest{AgentSlug: "fixture", RunID: "fixture-run", Credentials: creds}
		env, err := orchestrator.NativeCredentialEnvForTest(orchestrator.BuildEnvVarsSidecar(req, true), creds, files, true)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "sh", "-c", `test -n "$NATIVE_SSH_PATH" && test -f "$NATIVE_SSH_PATH" && exec ssh -F /dev/null -i "$NATIVE_SSH_PATH" -o IdentityAgent=none -o IdentitiesOnly=yes -o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o ClearAllForwardings=yes -o ConnectTimeout=3 -o UserKnownHostsFile="$1" -p "$2" fixture@127.0.0.1 whoami`, "fixture", known, fmt.Sprint(port))
		command.Env = append([]string{"PATH=/usr/bin:/bin"}, env...)
		output, err := command.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != "fixture\\native-user" {
			t.Fatalf("native SSH failed after Vault resolution: %v %s", err, output)
		}
		fieldFile := filepath.Join(files, "NATIVE_SSH_KEY_PEM")
		fieldBytes, err := os.ReadFile(fieldFile)
		fieldInfo, statErr := os.Stat(fieldFile)
		if err != nil || statErr != nil || fieldInfo.Mode().Perm() != 0400 || string(fieldBytes) != canonical {
			t.Fatal("native secret field bytes or permissions changed")
		}
		fieldCommand := exec.CommandContext(ctx, "sh", "-c", `test -f "$NATIVE_SSH_KEY_PEM" && exec ssh -F /dev/null -i "$NATIVE_SSH_KEY_PEM" -o IdentityAgent=none -o IdentitiesOnly=yes -o BatchMode=yes -o PasswordAuthentication=no -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o ClearAllForwardings=yes -o ConnectTimeout=3 -o UserKnownHostsFile="$1" -p "$2" fixture@127.0.0.1 whoami`, "fixture", known, fmt.Sprint(port))
		fieldCommand.Env = command.Env
		fieldOutput, err := fieldCommand.CombinedOutput()
		if err != nil || strings.TrimSpace(string(fieldOutput)) != "fixture\\native-user" {
			t.Fatalf("native secret-field SSH authentication failed: %v %s", err, fieldOutput)
		}
		return filepath.Join(files, "ssh/NATIVE_SSH")
	}
	oldFile := authenticate(creds)
	cli(replacement, "credential", "rotate", created.ID, "--value-stdin", "--yes")
	rotated := resolve(agent)
	if len(rotated) != 1 || rotated[0].PlainValue != replacement {
		t.Fatal("rotation changed replacement bytes")
	}
	authenticate(rotated)
	before, err := os.ReadFile(oldFile)
	if err != nil || string(before) != canonical {
		t.Fatal("new run overwrote prior run material")
	}
	cli("", "credential", "unassign", created.ID, agent)
	cli("", "credential", "assign", created.ID, agent, "--env-var-name", "NATIVE_SSH", "--ttl", "1s")
	time.Sleep(1100 * time.Millisecond)
	if len(resolve(agent)) != 0 {
		t.Fatal("expired lease delivered credential")
	}
	cli("", "credential", "unassign", created.ID, agent)
	cli("", "credential", "assign", created.ID, agent, "--env-var-name", "NATIVE_SSH", "--ttl", "30m")
	cli("", "credential", "unassign", created.ID, agent)
	if len(resolve(agent)) != 0 {
		t.Fatal("revoked grant delivered credential")
	}
}
