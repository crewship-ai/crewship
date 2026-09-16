package main

// Acceptance for the "Pending approvals" section of `crewship me` and
// `crewship now`, driven through the BUILT BINARY against a REAL api.Router
// over a REAL migrated database.
//
// The quick-action fan-out used to decode GET /api/v1/approvals into a
// `{"data": [...]}` envelope. The handler has always answered
// `{"rows": [...], "status", "count", "has_more"}`, so the decode succeeded
// with an empty slice and the section rendered "(0)" for every workspace,
// including an OWNER looking at a queue with pending rows. The in-process
// stub tests stubbed the endpoint with `data` and stayed green — a stub can
// only confirm the CLI's own belief about the wire. Nothing here is faked:
// the row is written by internal/harbormaster, the router is the one
// cmd_start.go builds, and the CLI is a subprocess.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const (
	quickActionsWorkspaceID    = "cqaws000000000000001"
	quickActionsApprovalReason = "acceptance: pending approval must be visible"
)

// startQuickActionsServer seeds one workspace with an OWNER and a MEMBER
// (each holding a CLI token) and one pending approval, then returns a CLI
// config path per role and the approval id.
func startQuickActionsServer(t *testing.T) (ownerCfg, memberCfg, approvalID string) {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'QA', 'qa-ws')`, quickActionsWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('qa-owner', 'owner@qa-ex.com', 'Owner')`)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('qa-member', 'member@qa-ex.com', 'Member')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('qam-owner', ?, 'qa-owner', 'OWNER')`,
		quickActionsWorkspaceID)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('qam-member', ?, 'qa-member', 'MEMBER')`,
		quickActionsWorkspaceID)

	const ownerToken = "crewship_cli_qaowner0000000000000000000000"
	const memberToken = "crewship_cli_qamember000000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-qa-owner', 'qa-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-qa-member', 'qa-member', 't', ?, datetime('now'))`,
		sha256HexToken(memberToken))

	// The row the CLI must show: written by the same store the API reads.
	id, err := harbormaster.Enqueue(context.Background(), db, nil, harbormaster.Request{
		WorkspaceID: quickActionsWorkspaceID,
		RequestedBy: "qa-owner",
		Kind:        harbormaster.KindCustom,
		Reason:      quickActionsApprovalReason,
	})
	if err != nil {
		t.Fatalf("enqueue approval: %v", err)
	}

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	writeCfg := func(name, token string) string {
		cfgPath := filepath.Join(t.TempDir(), name)
		cfg := "server: " + srv.URL + "\nworkspace: " + quickActionsWorkspaceID +
			"\ntoken: " + token + "\nformat: table\n"
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return cfgPath
	}
	return writeCfg("owner.yaml", ownerToken), writeCfg("member.yaml", memberToken), id
}

func runQuickActionsCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// quickActionsApprovals decodes the `approvals` and `errors` sections of a
// `--format json` render.
func quickActionsApprovals(t *testing.T, out string) (ids []string, errs []string) {
	t.Helper()
	var v struct {
		Approvals []map[string]any `json:"approvals"`
		Errors    []string         `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	for _, a := range v.Approvals {
		ids = append(ids, str(a["id"]))
	}
	return ids, v.Errors
}

// TestAcceptance_QuickActions_PendingApprovalsAreListed is the regression
// pin: an OWNER with one pending approval must see it in both screens.
func TestAcceptance_QuickActions_PendingApprovalsAreListed(t *testing.T) {
	ownerCfg, _, approvalID := startQuickActionsServer(t)

	for _, cmdName := range []string{"me", "now"} {
		out, err := runQuickActionsCLI(t, ownerCfg, cmdName, "--format", "json")
		if err != nil {
			t.Fatalf("%s: %v\n%s", cmdName, err, out)
		}
		ids, errs := quickActionsApprovals(t, out)
		if len(errs) != 0 {
			t.Errorf("%s: unexpected partial errors: %v", cmdName, errs)
		}
		found := false
		for _, id := range ids {
			if id == approvalID {
				found = true
			}
		}
		if !found {
			t.Errorf("%s --format json: approval %s missing; approvals = %v\n%s", cmdName, approvalID, ids, out)
		}

		human, err := runQuickActionsCLI(t, ownerCfg, cmdName)
		if err != nil {
			t.Fatalf("%s (human): %v\n%s", cmdName, err, human)
		}
		if !strings.Contains(human, approvalID) {
			t.Errorf("%s: human render does not list approval %s:\n%s", cmdName, approvalID, human)
		}
		// The row has no title; the line must carry what it does have —
		// the kind and the reason — or it reads as "apr_… •".
		if !strings.Contains(human, "custom: "+quickActionsApprovalReason) {
			t.Errorf("%s: human render does not show the approval's kind and reason:\n%s", cmdName, human)
		}
		if strings.Contains(human, "[partial]") {
			t.Errorf("%s: human render carries a [partial] line:\n%s", cmdName, human)
		}
	}
}

// TestAcceptance_QuickActions_MemberSeesAbsenceNotAnError pins the other
// half of the contract against the real role gate: GET /api/v1/approvals is
// OWNER/ADMIN-only, and a MEMBER's 403 there renders as an empty section —
// never as a `[partial]` error line.
func TestAcceptance_QuickActions_MemberSeesAbsenceNotAnError(t *testing.T) {
	_, memberCfg, _ := startQuickActionsServer(t)

	for _, cmdName := range []string{"me", "now"} {
		out, err := runQuickActionsCLI(t, memberCfg, cmdName, "--format", "json")
		if err != nil {
			t.Fatalf("%s: %v\n%s", cmdName, err, out)
		}
		ids, errs := quickActionsApprovals(t, out)
		if len(ids) != 0 {
			t.Errorf("%s: a MEMBER must not see approvals, got %v", cmdName, ids)
		}
		for _, e := range errs {
			if strings.HasPrefix(e, "approvals:") {
				t.Errorf("%s: the approvals 403 leaked as an error: %q", cmdName, e)
			}
		}
	}
}
