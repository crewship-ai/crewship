package orchestrator_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Real native CLI, API, encrypted Vault, resolver and production environment
// reach standard curl. Adapter cases exercise their environment builders, not
// a model CLI; live OAuth authorization/refresh needs separate qualification.
func TestNativeOAuthVaultToStandardCurl(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Fatal("curl is required for native credential acceptance")
	}
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	db := testutil.MigratedDB(t).DB
	const workspace = "cnativews00000000000001"
	const agent = "cnativeagent00000000001"
	const peer = "cnativepeer000000000001"
	const token = "crewship_cli_synthetic_oauth_fixture_00000000000"
	const master = "synthetic-native-internal-token"
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte(token))
	seed(`INSERT INTO workspaces(id,name,slug) VALUES(?,'Native OAuth','native-oauth')`, workspace)
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
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
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
			t.Fatalf("native credential command failed: %v", err)
		}
		if input != "" && strings.Contains(string(out), input) {
			t.Fatal("credential value reached CLI output")
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
	initial := "synthetic-access-initial-" + strings.Repeat("a", 40)
	replacement := "synthetic-access-rotated-" + strings.Repeat("b", 40)
	out := cli(initial, "credential", "create", "--name", "NATIVE_OAUTH", "--type", "OAUTH2", "--provider", "NONE", "--env-var-name", "NATIVE_OAUTH_ACCESS", "--value-stdin")
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(out, &created) != nil || created.ID == "" {
		t.Fatal("native CLI returned no credential")
	}
	var sealed string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id=?`, created.ID).Scan(&sealed); err != nil || sealed == "" || strings.Contains(sealed, initial) {
		t.Fatal("access token not encrypted at rest")
	}
	if len(resolve(agent)) != 0 {
		t.Fatal("unassigned OAuth credential reached agent")
	}
	cli("", "credential", "assign", created.ID, agent, "--env-var-name", "NATIVE_OAUTH_ACCESS", "--ttl", "30m")
	if len(resolve(peer)) != 0 {
		t.Fatal("ungranted peer resolved OAuth credential")
	}
	var accepted atomic.Int32
	authenticate := func(value string) {
		t.Helper()
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+value {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			accepted.Add(1)
			io.WriteString(w, "native-oauth-accepted")
		}))
		defer fixture.Close()
		creds := resolve(agent)
		if len(creds) != 1 || creds[0].PlainValue != value || creds[0].EnvVarName != "NATIVE_OAUTH_ACCESS" || !creds[0].OAuthPrimary {
			t.Fatal("native OAuth resolution changed bytes or slot")
		}
		for _, adapter := range []string{"CLAUDE_CODE", "CODEX_CLI", "OPENCODE"} {
			t.Run(adapter, func(t *testing.T) {
				req := orchestrator.AgentRunRequest{AgentSlug: "fixture", RunID: "oauth-run", CLIAdapter: adapter, Credentials: creds}
				env, err := orchestrator.NativeCredentialEnvForTest(orchestrator.BuildEnvVarsSidecar(req, true), creds, t.TempDir(), true)
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(t.Context(), "sh", "-c", `test -n "$NATIVE_OAUTH_ACCESS" && printf 'header = "Authorization: Bearer %s"\n' "$NATIVE_OAUTH_ACCESS" | curl --config - --silent --show-error --fail --max-time 3 "$1"`, "fixture", fixture.URL)
				cmd.Env = append([]string{"PATH=/usr/bin:/bin", "NO_PROXY=127.0.0.1"}, env...)
				got, err := cmd.CombinedOutput()
				if err != nil || strings.TrimSpace(string(got)) != "native-oauth-accepted" {
					t.Fatalf("standard curl did not authenticate with the native slot: %v", err)
				}
			})
		}
	}
	authenticate(initial)
	cli(replacement, "credential", "rotate", created.ID, "--value-stdin", "--yes")
	authenticate(replacement)
	if accepted.Load() != 6 {
		t.Fatalf("accepted upstream requests=%d, want6", accepted.Load())
	}
	cli("", "credential", "unassign", created.ID, agent)
	if len(resolve(agent)) != 0 {
		t.Fatal("ungranted OAuth credential remains accessible")
	}
	cli("", "credential", "assign", created.ID, agent, "--env-var-name", "NATIVE_OAUTH_ACCESS", "--ttl", "1s")
	time.Sleep(1100 * time.Millisecond)
	if len(resolve(agent)) != 0 {
		t.Fatal("expired OAuth grant remains accessible")
	}
	cli("", "credential", "unassign", created.ID, agent)
}
