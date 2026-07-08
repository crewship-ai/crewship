package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/database"
)

// cmd_system_keeper_integration_test.go — regression guard for #896.
//
// `crewship system keeper` used to clear client.WorkspaceID before the GET,
// which was fine while /api/v1/system/keeper was instance-wide. #893 moved the
// route behind authedAdmin (RequireAuth → RequireWorkspace → roleManage), and
// RequireWorkspace hard-400s when no workspace is supplied — so the command
// 400'd for everyone, OWNER included.
//
// The existing cov test stubs the HTTP response, so it never exercised the real
// middleware chain and the regression was structurally invisible. These tests
// drive the actual command against a REAL api.NewRouter router so the workspace
// gate runs for real: OWNER must get through, MEMBER must get a clean 403.

// keeperTestWorkspaceID is a CUID-shaped id so the CLI client treats it as an
// already-resolved workspace id and does not fire a slug→id resolution request.
const keeperTestWorkspaceID = "ckeeperws00000000001a"

// sha256HexToken mirrors the server's hashStandard: plain SHA-256 hex of the
// cleartext CLI token (the cleartext is already high-entropy).
func sha256HexToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// setupKeeperRouterServer builds a real router over a migrated SQLite DB, seeds
// a workspace with an OWNER and a MEMBER (each with a CLI token), and returns
// the running test server plus the two plaintext tokens.
func setupKeeperRouterServer(t *testing.T) (server *httptest.Server, ownerToken, memberToken string) {
	t.Helper()

	dir := t.TempDir()
	dbh, err := database.Open("file:" + filepath.Join(dir, "keeper-it.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := database.Migrate(context.Background(), dbh.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	db := dbh.DB
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}

	// Workspace + OWNER + MEMBER.
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Keeper IT', 'keeper-it')`, keeperTestWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('kp-owner', 'owner@ex.com', 'Owner')`)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('kp-member', 'member@ex.com', 'Member')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('kpm-owner', ?, 'kp-owner', 'OWNER')`, keeperTestWorkspaceID)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('kpm-member', ?, 'kp-member', 'MEMBER')`, keeperTestWorkspaceID)

	ownerToken = "crewship_cli_kpowner00000000000000000000"
	memberToken = "crewship_cli_kpmember0000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-owner', 'kp-owner', 't', ?, datetime('now'))`, sha256HexToken(ownerToken))
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-member', 'kp-member', 't', ?, datetime('now'))`, sha256HexToken(memberToken))

	r, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, ownerToken, memberToken
}

// TestSystemKeeper_OwnerReachesRealRouter is the RED test: on current main it
// fails because the command clears the workspace and RequireWorkspace 400s.
func TestSystemKeeper_OwnerReachesRealRouter(t *testing.T) {
	saveCLIState(t)
	srv, ownerToken, _ := setupKeeperRouterServer(t)

	cliCfg = &cli.CLIConfig{
		Token:     ownerToken,
		Workspace: keeperTestWorkspaceID,
		Server:    srv.URL,
	}

	if err := systemKeeperCmd.RunE(systemKeeperCmd, nil); err != nil {
		t.Fatalf("OWNER `system keeper` should succeed against the real router, got: %v", err)
	}
}

// TestSystemKeeper_MemberGetsCleanForbidden pins the acceptance that a MEMBER
// gets a clean 403 (permission) explanation, not the bare 400 the regression
// produced for everyone.
func TestSystemKeeper_MemberGetsCleanForbidden(t *testing.T) {
	saveCLIState(t)
	srv, _, memberToken := setupKeeperRouterServer(t)

	cliCfg = &cli.CLIConfig{
		Token:     memberToken,
		Workspace: keeperTestWorkspaceID,
		Server:    srv.URL,
	}

	err := systemKeeperCmd.RunE(systemKeeperCmd, nil)
	if err == nil {
		t.Fatal("MEMBER `system keeper` should be forbidden, got nil error")
	}
	msg := err.Error()
	if strings.Contains(msg, "400") || strings.Contains(strings.ToLower(msg), "workspace_id is required") {
		t.Fatalf("MEMBER got the bare-400 regression instead of a clean 403: %v", err)
	}
	if !strings.Contains(msg, "403") {
		t.Fatalf("MEMBER error should surface a 403 permission failure, got: %v", err)
	}
}
