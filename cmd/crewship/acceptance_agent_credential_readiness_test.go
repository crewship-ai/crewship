package main

// Acceptance for `crewship agent credential-readiness`, driven through the
// BUILT BINARY against a REAL api.Router over a REAL migrated database
// (#2183). Nothing is stubbed: a stub can only confirm the CLI's own belief
// about the wire, and this command exists because two guards built on the
// wrong belief shipped green.
//
// The seed is the dev3 measurement from the issue: a crew whose only binding
// is a GH_TOKEN, one agent with nothing else (parity-probe), one agent with a
// Claude Code login granted directly (casey).

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const acrWorkspaceID = "cacrws00000000000001a"

func startAgentReadinessServer(t *testing.T) string {
	t.Helper()

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Readiness', 'readiness-ws')`, acrWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('acr-owner', 'owner@acr-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('acrm-owner', ?, 'acr-owner', 'OWNER')`, acrWorkspaceID)
	const ownerToken = "crewship_cli_acrowner0000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-acr-owner', 'acr-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus, created_at)
		VALUES ('crew-quality', ?, 'Quality', 'quality', 'free', 4096, 2.0, '2026-01-01T00:00:01Z')`, acrWorkspaceID)
	for _, a := range []struct{ id, slug string }{{"agent-probe", "parity-probe"}, {"agent-casey", "casey"}} {
		mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled, created_at)
			VALUES (?, ?, 'crew-quality', ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-5', 'CODING', 1800, 0, '2026-01-01T00:00:01Z')`,
			a.id, acrWorkspaceID, a.slug, a.slug)
	}

	// The value column is never opened by this route; an opaque blob is
	// enough and proves that by construction.
	mustExec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-gh', ?, 'github-globex', 'opaque', 'CLI_TOKEN', 'GITHUB', 'CREW', 'ACTIVE', 'acr-owner', '2026-01-01 00:00:01', '2026-01-01 00:00:01')`, acrWorkspaceID)
	mustExec(`INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, crew_id, created_at)
		VALUES ('cb-gh', ?, 'cred-gh', 'GH_TOKEN', 'CREW', 'crew-quality', datetime('now'))`, acrWorkspaceID)
	mustExec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-claude', ?, 'CLAUDE_CODE_OAUTH_TOKEN', 'opaque', 'AI_CLI_TOKEN', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', 'acr-owner', '2026-01-01 00:00:01', '2026-01-01 00:00:01')`, acrWorkspaceID)
	mustExec(`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		VALUES ('ac-casey', 'agent-casey', 'cred-claude', 'CLAUDE_CODE_OAUTH_TOKEN', 0, datetime('now'))`)

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + acrWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func runAgentReadinessCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptance_AgentCredentialReadiness_BindingOnlyAgentIsMissing(t *testing.T) {
	cfgPath := startAgentReadinessServer(t)

	// The listing the old guard read: one row, so it looked "assigned".
	listOut, err := runAgentReadinessCLI(t, cfgPath, "agent", "credentials", "parity-probe")
	if err != nil {
		t.Fatalf("agent credentials: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "github-globex") {
		t.Fatalf("the GH_TOKEN binding should reach parity-probe:\n%s", listOut)
	}

	out, err := runAgentReadinessCLI(t, cfgPath, "agent", "credential-readiness", "parity-probe")
	if err != nil {
		t.Fatalf("agent credential-readiness: %v\n%s", err, out)
	}
	for _, want := range []string{"parity-probe", "CLAUDE_CODE", "NO model credential", "ANTHROPIC", "credential assign"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	jsonOut, err := runAgentReadinessCLI(t, cfgPath, "agent", "credential-readiness", "parity-probe", "--format", "json")
	if err != nil {
		t.Fatalf("agent credential-readiness --format json: %v\n%s", err, jsonOut)
	}
	var rep agentCredentialReadinessOut
	if err := json.Unmarshal([]byte(jsonOut), &rep); err != nil {
		t.Fatalf("json output does not parse: %v\n%s", err, jsonOut)
	}
	if rep.ModelCredential.State != "missing" || rep.ModelCredential.Provider != "ANTHROPIC" || rep.Adapter != "CLAUDE_CODE" {
		t.Errorf("json report = %+v, want missing/ANTHROPIC/CLAUDE_CODE", rep)
	}
	if rep.Notes == nil {
		t.Errorf("notes must be an array, got null:\n%s", jsonOut)
	}
}

func TestAcceptance_AgentCredentialReadiness_GrantedLoginIsReady(t *testing.T) {
	cfgPath := startAgentReadinessServer(t)

	out, err := runAgentReadinessCLI(t, cfgPath, "agent", "credential-readiness", "casey")
	if err != nil {
		t.Fatalf("agent credential-readiness: %v\n%s", err, out)
	}
	for _, want := range []string{"casey", "ready", "CLAUDE_CODE_OAUTH_TOKEN", "agent_grant", "login_env"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "opaque") {
		t.Errorf("a credential value reached the output:\n%s", out)
	}

	jsonOut, err := runAgentReadinessCLI(t, cfgPath, "agent", "credential-readiness", "casey", "-f", "json")
	if err != nil {
		t.Fatalf("-f json: %v\n%s", err, jsonOut)
	}
	var rep agentCredentialReadinessOut
	if err := json.Unmarshal([]byte(jsonOut), &rep); err != nil {
		t.Fatalf("json output does not parse: %v\n%s", err, jsonOut)
	}
	if rep.ModelCredential.State != "ready" || rep.ModelCredential.Source != "agent_grant" || rep.ModelCredential.CredentialName != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("json report = %+v", rep)
	}
}

func TestAcceptance_AgentCredentialReadiness_UnknownAgentFails(t *testing.T) {
	cfgPath := startAgentReadinessServer(t)
	out, err := runAgentReadinessCLI(t, cfgPath, "agent", "credential-readiness", "ghost")
	if err == nil {
		t.Fatalf("expected a failure for an unknown agent, got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "not found") {
		t.Errorf("error should say the agent was not found:\n%s", out)
	}
}
