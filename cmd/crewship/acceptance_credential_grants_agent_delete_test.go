package main

// Acceptance for #5529, the exact sequence from the issue, driven through the
// BUILT BINARY against a real api.Router over a real migrated database:
//
//	crewship credential assign CLAUDE_CODE_OAUTH_TOKEN <agent> --ttl 2h --env-var-name CLAUDE_CODE_OAUTH_TOKEN
//	crewship agent delete <agent> --yes
//	crewship credential list        → AGENTS stayed at the pre-delete count
//
// and `credential unassign` could not remove the leftover grant because the
// deleted agent is "not found". After the fix the delete takes the grant with
// it, the count drops back, and a bystander agent's grant on the same
// credential stays deliverable.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const cgdWorkspaceID = "ccgdws00000000000001a"

func TestAcceptance_AgentDeleteRevokesItsCredentialGrants(t *testing.T) {
	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Grants', 'grants-ws')`, cgdWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('cgd-owner', 'owner@cgd-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('cgdm-owner', ?, 'cgd-owner', 'OWNER')`, cgdWorkspaceID)
	const ownerToken = "crewship_cli_cgdowner0000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-cgd-owner', 'cgd-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus, created_at)
		VALUES ('crew-cgd', ?, 'Grants', 'grants', 'free', 4096, 2.0, '2026-01-01T00:00:01Z')`, cgdWorkspaceID)
	for _, a := range []struct{ id, slug string }{{"agent-leaving", "leaving"}, {"agent-staying", "staying"}} {
		mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled, created_at)
			VALUES (?, ?, 'crew-cgd', ?, ?, 'AGENT', 'IDLE', 'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-5', 'CODING', 1800, 0, '2026-01-01T00:00:01Z')`,
			a.id, cgdWorkspaceID, a.slug, a.slug)
	}
	// The value column is never opened on these routes; an opaque blob proves
	// it stays exactly as it was.
	mustExec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-cgd', ?, 'CLAUDE_CODE_OAUTH_TOKEN', 'opaque-material', 'AI_CLI_TOKEN', 'ANTHROPIC', 'WORKSPACE', 'ACTIVE', 'cgd-owner', '2026-01-01 00:00:01', '2026-01-01 00:00:01')`, cgdWorkspaceID)

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + cgdWorkspaceID + "\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	run := func(args ...string) (string, error) {
		t.Helper()
		return runAgentReadinessCLI(t, cfgPath, args...)
	}
	mustRun := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("crewship %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	agentCount := func() int {
		t.Helper()
		out := mustRun("credential", "list", "-f", "json")
		var list []struct {
			ID         string `json:"id"`
			AgentCount int    `json:"_count_agent_credentials"`
		}
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatalf("decode credential list: %v\n%s", err, out)
		}
		for _, c := range list {
			if c.ID == "cred-cgd" {
				return c.AgentCount
			}
		}
		t.Fatalf("cred-cgd not in credential list:\n%s", out)
		return 0
	}

	mustRun("credential", "assign", "CLAUDE_CODE_OAUTH_TOKEN", "staying", "--env-var-name", "CLAUDE_CODE_OAUTH_TOKEN")
	mustRun("credential", "assign", "CLAUDE_CODE_OAUTH_TOKEN", "leaving", "--ttl", "2h", "--env-var-name", "CLAUDE_CODE_OAUTH_TOKEN")
	if got := agentCount(); got != 2 {
		t.Fatalf("after two assigns AGENTS = %d, want 2", got)
	}

	mustRun("agent", "delete", "leaving", "--yes")

	if got := agentCount(); got != 1 {
		t.Errorf("after deleting a grantee AGENTS = %d, want 1 — the deleted agent's grant still counts (#5529)", got)
	}
	var leftover int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agent_credentials WHERE agent_id = 'agent-leaving'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Errorf("%d grant rows outlived agent-leaving; nothing in the CLI can remove them", leftover)
	}

	// The bystander keeps its grant, and it is still listed as explicit.
	out := mustRun("agent", "credentials", "staying", "-f", "json")
	if !strings.Contains(out, `"cred-cgd"`) || !strings.Contains(out, `"explicit"`) {
		t.Errorf("staying lost its grant on the same credential:\n%s", out)
	}

	// The revoke is on the credential's audit timeline, attributed to the agent.
	var audits int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id = 'cred-cgd'
		AND event_type = 'REVOKE' AND agent_id = 'agent-leaving'
		AND json_extract(metadata_json, '$.reason') = 'agent_deleted'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("REVOKE audit rows for the deleted agent's grant = %d, want 1", audits)
	}

	// The credential itself is untouched.
	var material string
	var deletedAt *string
	if err := db.QueryRow(`SELECT encrypted_value, deleted_at FROM credentials WHERE id = 'cred-cgd'`).Scan(&material, &deletedAt); err != nil {
		t.Fatalf("credential vanished: %v", err)
	}
	if material != "opaque-material" || deletedAt != nil {
		t.Errorf("credential changed by agent delete: material=%q deleted_at=%v", material, deletedAt)
	}
}
