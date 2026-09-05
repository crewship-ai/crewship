package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Acceptance for PRD §18 scenario 10 (B14, #2388): a peer agent's "GO"
// cannot satisfy a waitpoint; a person can. The server is the real
// api.NewRouter with the real waitpoint store; the peer agent is an HTTP
// client presenting the crew-bound X-Internal-Token its sidecar holds —
// the only credential an agent has — and the person is the CLI binary
// with an owner's CLI token, which is the contract agents' operators use.

const waitpointDeciderAcceptanceWorkspaceID = "cwpdeciderws0000000001"

func startWaitpointDeciderAcceptanceServer(t *testing.T) (srvURL, cfgPath, token, crewToken string, db *sql.DB) {
	t.Helper()
	dbh := testutil.MigratedDB(t)
	sqlDB := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	const ws = waitpointDeciderAcceptanceWorkspaceID
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Decider', 'decider-ws')`, ws)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('wpd-owner', 'owner@wpd-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wpdm-owner', ?, 'wpd-owner', 'OWNER')`, ws)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('wpd-crew', ?, 'Crew', 'wpd-crew', 'free', 4096, 2.0)`, ws)
	const ownerToken = "crewship_cli_wpdowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-wpd-owner', 'wpd-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	const master = "acceptance-internal-token-master-32-bytes!"
	router, err := api.NewRouter(sqlDB, "this-is-a-32-char-test-secret-pad", logger, api.WithInternalToken(master))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	store := pipeline.NewSQLWaitpointStore(sqlDB)
	t.Cleanup(store.Close)
	router.PipelinesHandler.SetWaitpointStore(store)
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	tok, err := store.CreateApproval(context.Background(), pipeline.WaitpointApprovalRequest{
		WorkspaceID: ws, PipelineRunID: "run-wpd", StepID: "gate", Prompt: "Publish the release?", TimeoutSec: 3600,
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + ws + "\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return srv.URL, cfgPath, tok, internaltoken.DeriveCrewToken(master, ws, "wpd-crew"), sqlDB
}

func runWaitpointDeciderCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptance_Waitpoint_PeerAgentGOIsRefused_PersonDecidesViaCLI(t *testing.T) {
	srvURL, cfgPath, tok, crewToken, db := startWaitpointDeciderAcceptanceServer(t)
	const ws = waitpointDeciderAcceptanceWorkspaceID
	approveURL := srvURL + "/api/v1/workspaces/" + ws + "/pipelines/waitpoints/" + tok + "/approve"

	rowStatus := func(t *testing.T) (status, by string) {
		t.Helper()
		var byp *string
		if err := db.QueryRow(`SELECT status, decided_by_user_id FROM pipeline_waitpoints WHERE token = ?`, tok).Scan(&status, &byp); err != nil {
			t.Fatalf("read waitpoint: %v", err)
		}
		if byp != nil {
			by = *byp
		}
		return status, by
	}

	// 1. The peer agent says GO with the only credential it has — its
	//    crew-bound internal token — on the authed approve route, and again
	//    with no credential at all. Both are turned away and the waitpoint
	//    stays pending.
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"peer agent's crew-bound internal token", crewToken},
		{"no credential", ""},
	} {
		req, _ := http.NewRequest("POST", approveURL, bytes.NewBufferString(`{"approved":true,"comment":"GO"}`))
		req.Header.Set("Content-Type", "application/json")
		if tc.token != "" {
			req.Header.Set("X-Internal-Token", tc.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 401 or 403", tc.name, resp.StatusCode)
		}
		if status, by := rowStatus(t); status != "pending" || by != "" {
			t.Fatalf("%s: waitpoint = (%s, %q), want (pending, \"\")", tc.name, status, by)
		}
	}

	// 2. The waitpoint is still on the person's list.
	out, err := runWaitpointDeciderCLI(t, cfgPath, "routine", "waitpoints", "list", "--format", "json")
	if err != nil {
		t.Fatalf("waitpoints list: %v\n%s", err, out)
	}
	if !strings.Contains(out, tok) {
		t.Fatalf("pending waitpoint %s not listed:\n%s", tok, out)
	}

	// 3. A person decides through the CLI, and the decision is theirs.
	out, err = runWaitpointDeciderCLI(t, cfgPath, "routine", "waitpoints", "approve", tok, "--comment", "LGTM")
	if err != nil {
		t.Fatalf("waitpoints approve: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Approved waitpoint") {
		t.Fatalf("approve output:\n%s", out)
	}
	if status, by := rowStatus(t); status != "approved" || by != "wpd-owner" {
		t.Fatalf("waitpoint after CLI approve = (%s, %q), want (approved, wpd-owner)", status, by)
	}
}
