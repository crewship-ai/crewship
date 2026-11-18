//go:build integration

package orchestrator_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Create, rotate, grant and resolve through the native CLI and real Vault,
// then use those actual results in each real CLI's shell against the isolated
// SSH/mTLS/basic/bearer/GitLab services. There is no credential API stub.
func TestNativeVaultCLITrioProtocols(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	binary := filepath.Join(t.TempDir(), "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture CLI: %v %s", err, out)
	}
	for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "OPENCODE"} {
		t.Run(adapter, func(t *testing.T) {
			const workspace = "cnativews00000000000001"
			const agent = "cnativeagent00000000001"
			const peer = "cnativepeer000000000001"
			const token = "crewship_cli_synthetic_native_fixture_0000000000"
			const master = "synthetic-native-internal-token"
			db := testutil.MigratedDB(t).DB
			seed := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256([]byte(token))
			seed(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Native fixture','native-fixture')`, workspace)
			seed(`INSERT INTO users(id,email,full_name) VALUES('native-owner','owner@native.invalid','Fixture')`)
			seed(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('native-member',?,'native-owner','OWNER')`, workspace)
			seed(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('native-token','native-owner','fixture',?,datetime('now'))`, hex.EncodeToString(sum[:]))
			seed(`INSERT INTO crews(id,workspace_id,name,slug,network_mode,container_memory_mb,container_cpus,created_at) VALUES('native-crew',?,'Native','native','free',512,1,datetime('now'))`, workspace)
			for _, id := range []string{agent, peer} {
				seed(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,status,cli_adapter,llm_provider,llm_model,tool_profile,timeout_seconds,memory_enabled,created_at) VALUES(?,?,'native-crew',?,?,'AGENT','IDLE',?,'ANTHROPIC','claude-sonnet-5-5','CODING',60,0,datetime('now'))`, id, workspace, id, id, adapter)
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger, api.WithInternalToken(master))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(router)
			defer server.Close()
			config := filepath.Join(t.TempDir(), "cli.yaml")
			if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cli := func(input string, args ...string) []byte {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), binary, args...)
				for _, entry := range os.Environ() {
					if !strings.HasPrefix(entry, "CREWSHIP_") {
						cmd.Env = append(cmd.Env, entry)
					}
				}
				cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
				cmd.Stdin = strings.NewReader(input)
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("native CLI %q failed: %v", args, err)
				}
				if input != "" && strings.Contains(string(out), input) {
					t.Fatal("native CLI disclosed credential input")
				}
				return out
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
			contains := func(creds []orchestrator.Credential, id string) bool {
				for _, c := range creds {
					if c.ID == id {
						return true
					}
				}
				return false
			}
			var ids []string
			orchestrator.NativeCLIShellWithVaultForTest(t, adapter, "direct", func(expected []orchestrator.Credential) []orchestrator.Credential {
				for i, c := range expected {
					initial := c.PlainValue + "\n"
					if c.Type != "SSH_KEY" && c.Type != "CERTIFICATE" {
						initial = "pre-rotation-" + c.PlainValue
					}
					args := []string{"credential", "create", "--name", fmt.Sprintf("NATIVE_CHAIN_%d", i), "--type", c.Type, "--provider", "NONE", "--env-var-name", c.EnvVarName, "--security-level", "3"}
					inputFlag := "--value-stdin"
					if c.Type == "USERPASS" {
						inputFlag = "--password-stdin"
						args = append(args, "--username", c.Username)
					}
					args = append(args, inputFlag)
					var created struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(cli(initial, args...), &created); err != nil || created.ID == "" {
						t.Fatal("native create returned no credential id")
					}
					ids = append(ids, created.ID)
					if contains(resolve(agent), created.ID) {
						t.Fatal("new unassigned credential resolved")
					}
					// A different synthetic initial value must actually change on rotation.
					cli(c.PlainValue, "credential", "rotate", created.ID, inputFlag, "--yes")
					var encrypted string
					if err := db.QueryRowContext(t.Context(), `SELECT encrypted_value FROM credentials WHERE id=?`, created.ID).Scan(&encrypted); err != nil {
						t.Fatal(err)
					}
					if encrypted == "" || strings.Contains(encrypted, c.PlainValue) {
						t.Fatal("primary value was not encrypted at rest")
					}
					for _, field := range c.Fields {
						if field.EnvVar != c.EnvVarName+"_KEY_PEM" || !field.IsSecret {
							t.Fatal("new fixture field needs explicit native CLI mapping")
						}
						cli(field.Value, "credential", "field", "set", created.ID, "key_pem", "--value-stdin")
						var plain, sealed string
						if err := db.QueryRowContext(t.Context(), `SELECT COALESCE(value,''),encrypted_value FROM credential_fields WHERE credential_id=? AND key='key_pem'`, created.ID).Scan(&plain, &sealed); err != nil || plain != "" || sealed == "" || strings.Contains(sealed, field.Value) {
							t.Fatal("key_pem field was not encrypted at rest")
						}
					}
					cli("", "credential", "assign", created.ID, agent, "--env-var-name", c.EnvVarName, "--ttl", "30m")
					if !contains(resolve(agent), created.ID) {
						t.Fatal("assigned credential missing")
					}
					if contains(resolve(peer), created.ID) {
						t.Fatal("ungranted peer resolved credential")
					}
					cli("", "credential", "unassign", created.ID, agent)
					if contains(resolve(agent), created.ID) {
						t.Fatal("revoked grant still resolved")
					}
					cli("", "credential", "assign", created.ID, agent, "--env-var-name", c.EnvVarName, "--ttl", "30m")
				}
				actual := resolve(agent)
				if len(actual) != len(expected) {
					t.Fatal("wrong native credential count")
				}
				for _, want := range expected {
					var got *orchestrator.Credential
					for i := range actual {
						if actual[i].EnvVarName == want.EnvVarName {
							got = &actual[i]
							break
						}
					}
					if got == nil || got.Type != want.Type || got.PlainValue != want.PlainValue || got.Username != want.Username || len(got.Fields) != len(want.Fields) {
						t.Fatal("Vault/resolver changed type, slot, bytes or field count")
					}
					for i, f := range want.Fields {
						if got.Fields[i].EnvVar != f.EnvVar || got.Fields[i].Value != f.Value || got.Fields[i].IsSecret != f.IsSecret {
							t.Fatal("Vault/resolver changed secret field bytes or alias")
						}
					}
				}
				return actual
			})
			for _, id := range ids {
				cli("", "credential", "unassign", id, agent)
			}
			if len(resolve(agent)) != 0 {
				t.Fatal("post-tool native grant cleanup failed")
			}
		})
	}
}
