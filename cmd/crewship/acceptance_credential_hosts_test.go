package main

// Acceptance for `crewship credential hosts` (#5045), driven through the BUILT
// BINARY against a REAL api.Router over a REAL migrated database.

import (
	"database/sql"
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

const chWorkspaceID = "cchws00000000000001a"

func startCredentialHostsServer(t *testing.T) (ownerCfg, memberCfg string, db *sql.DB) {
	t.Helper()
	db = testutil.MigratedDB(t).DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Hosts', 'hosts-ws')`, chWorkspaceID)
	for _, u := range []struct{ id, role, tok string }{
		{"chh-owner", "OWNER", "crewship_cli_chhowner00000000000000000"},
		{"chh-member", "MEMBER", "crewship_cli_chhmember0000000000000000"},
	} {
		mustExec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`, u.id, u.id+"@ch-ex.com", u.id)
		mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, ?)`, "m-"+u.id, chWorkspaceID, u.id, u.role)
		mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, 't', ?, datetime('now'))`,
			"clt-"+u.id, u.id, sha256HexToken(u.tok))
	}
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus, created_at)
		VALUES ('crew-h', ?, 'H', 'h', 'free', 4096, 2.0, '2026-01-01T00:00:01Z')`, chWorkspaceID)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, llm_provider, llm_model, tool_profile, timeout_seconds, memory_enabled, created_at)
		VALUES ('agent-h', ?, 'crew-h', 'hal', 'hal', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'ANTHROPIC', 'claude-sonnet-4-5', 'CODING', 1800, 0, '2026-01-01T00:00:01Z')`, chWorkspaceID)
	mustExec(`INSERT INTO credentials (id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
		VALUES ('cred-h', ?, 'gh-token', 'opaque', 'CLI_TOKEN', 'GITHUB', 'CREW', 'ACTIVE', 'chh-owner', '2026-01-01 00:00:01', '2026-01-01 00:00:01')`, chWorkspaceID)
	mustExec(`INSERT INTO credential_bindings (id, workspace_id, credential_id, slot, scope, crew_id, created_at)
		VALUES ('cb-h', ?, 'cred-h', 'GH_TOKEN', 'CREW', 'crew-h', datetime('now'))`, chWorkspaceID)

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfg := func(tok string) string {
		p := filepath.Join(t.TempDir(), "cli-config.yaml")
		body := "server: " + srv.URL + "\nworkspace: " + chWorkspaceID + "\ntoken: " + tok + "\nformat: table\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return cfg("crewship_cli_chhowner00000000000000000"), cfg("crewship_cli_chhmember0000000000000000"), db
}

func TestAcceptance_CredentialHosts(t *testing.T) {
	ownerCfg, memberCfg, db := startCredentialHostsServer(t)
	run := func(cfg string, args ...string) (string, error) { return runAgentReadinessCLI(t, cfg, args...) }

	// Unbound by default.
	out, err := run(ownerCfg, "credential", "hosts", "gh-token")
	if err != nil || !strings.Contains(out, "No approved hosts") {
		t.Fatalf("show: %v\n%s", err, out)
	}

	// Malformed input is refused and nothing is stored.
	for _, bad := range []string{"https://api.github.com", "*", "10.0.0.1", "a.com:443"} {
		if out, err := run(ownerCfg, "credential", "hosts", "gh-token", "--set", bad); err == nil {
			t.Errorf("--set %q should fail:\n%s", bad, out)
		}
	}
	// A non-owner cannot set.
	if out, err := run(memberCfg, "credential", "hosts", "gh-token", "--set", "api.github.com"); err == nil {
		t.Fatalf("a MEMBER must not set hosts:\n%s", out)
	}
	var stored string
	_ = db.QueryRow(`SELECT allowed_hosts FROM credentials WHERE id='cred-h'`).Scan(&stored)
	if stored != "[]" {
		t.Fatalf("refused calls changed the column: %s", stored)
	}

	// Owner sets; normalisation applies; the limit of the guarantee is printed.
	out, err = run(ownerCfg, "credential", "hosts", "gh-token", "--set", "API.GitHub.com.,*.GitHubusercontent.com")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	for _, want := range []string{"api.github.com", "*.githubusercontent.com", "does not stop"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(strings.ToLower(out), "per-credential egress") {
		t.Errorf("output must not claim per-credential egress enforcement:\n%s", out)
	}

	// Free crew: the bound credential is withheld and readiness says so.
	js, err := run(ownerCfg, "agent", "credential-readiness", "hal", "--format", "json")
	if err != nil {
		t.Fatalf("readiness: %v\n%s", err, js)
	}
	var rep agentCredentialReadinessOut
	if err := json.Unmarshal([]byte(js), &rep); err != nil {
		t.Fatalf("json: %v\n%s", err, js)
	}
	if len(rep.HostBoundWithheld) != 1 || rep.HostBoundWithheld[0].CredentialName != "gh-token" {
		t.Fatalf("want gh-token withheld, got %+v", rep.HostBoundWithheld)
	}
	human, _ := run(ownerCfg, "agent", "credential-readiness", "hal")
	if !strings.Contains(human, "Withheld from delivery") {
		t.Errorf("human readiness output lacks the withheld section:\n%s", human)
	}

	// Restricted crew that allows both hosts: delivered, nothing withheld.
	if _, err := db.Exec(`UPDATE crews SET network_mode='restricted', allowed_domains='["api.github.com","*.githubusercontent.com"]' WHERE id='crew-h'`); err != nil {
		t.Fatal(err)
	}
	js, _ = run(ownerCfg, "agent", "credential-readiness", "hal", "--format", "json")
	rep = agentCredentialReadinessOut{}
	if err := json.Unmarshal([]byte(js), &rep); err != nil || len(rep.HostBoundWithheld) != 0 {
		t.Fatalf("satisfied crew: %v %+v", err, rep.HostBoundWithheld)
	}

	// Clearing unbinds.
	out, err = run(ownerCfg, "credential", "hosts", "gh-token", "--clear")
	if err != nil || !strings.Contains(out, "No approved hosts") {
		t.Fatalf("clear: %v\n%s", err, out)
	}
}
