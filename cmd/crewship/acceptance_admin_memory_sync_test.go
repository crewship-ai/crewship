package main

// #1702 acceptance — `crewship admin memory sync user-model|peer-cards`
// driven through the BUILT BINARY against the REAL api router.
//
//   - POST /api/v1/admin/memory/user-model-sync → admin memory sync user-model
//   - POST /api/v1/admin/memory/peer-card-sync  → admin memory sync peer-cards
//
// The router is the production one, storage root included, so the run below
// is the real sweep with the real extractor wiring: with no curator slot
// configured the user-model extractor returns nothing, which the sweep
// records as skipped_empty — the exact outcome #1702 says was invisible until
// 05:00 UTC. The peer-card sweep has no extractor at all on the server, so it
// too reports the candidate as empty. Neither writes a file, which is what
// makes them safe to drive for real here; a dry run must look the same on
// disk AND say so.

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const memorySyncAcceptanceWorkspaceID = "cmemsyncws000000000001a"

// startMemorySyncAcceptanceServer builds a real router over a migrated DB
// holding one workspace, an OWNER and a MEMBER each with a CLI token, one
// crew, one agent, and one chat by the owner that crosses the interaction
// threshold — one candidate for each sweep. It returns the server URL, a
// config path per role, and the storage root the sweeps write under.
func startMemorySyncAcceptanceServer(t *testing.T) (ownerCfg, memberCfg, storageRoot string) {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Memory Sync', 'memsync-ws')`, memorySyncAcceptanceWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('ms-owner', 'owner@ms-ex.com', 'Owner'), ('ms-member', 'member@ms-ex.com', 'Member')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('msm-owner', ?, 'ms-owner', 'OWNER'), ('msm-member', ?, 'ms-member', 'MEMBER')`,
		memorySyncAcceptanceWorkspaceID, memorySyncAcceptanceWorkspaceID)
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus)
		VALUES ('ms-crew', ?, 'Crew', 'ms-crew', 'free', 4096, 2.0)`, memorySyncAcceptanceWorkspaceID)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status,
		cli_adapter, tool_profile, timeout_seconds, memory_enabled)
		VALUES ('ms-agent', ?, 'ms-crew', 'Agent', 'ms-agent', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0)`,
		memorySyncAcceptanceWorkspaceID)
	start := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Format(time.RFC3339)
	mustExec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title, message_count, started_at, ended_at)
		VALUES ('ms-chat', 'ms-agent', ?, 'ms-owner', 'Long chat', 20, ?, ?)`,
		memorySyncAcceptanceWorkspaceID, start, end)

	const ownerToken = "crewship_cli_msowner000000000000000000000"
	const memberToken = "crewship_cli_msmember00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-ms-owner', 'ms-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-ms-member', 'ms-member', 't', ?, datetime('now'))`,
		sha256HexToken(memberToken))

	storageRoot = t.TempDir()
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger,
		api.WithOutputBasePath(storageRoot))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	writeCfg := func(name, token string) string {
		p := filepath.Join(t.TempDir(), name)
		cfg := "server: " + srv.URL + "\nworkspace: " + memorySyncAcceptanceWorkspaceID +
			"\ntoken: " + token + "\nformat: table\n"
		if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
		return p
	}
	return writeCfg("owner.yaml", ownerToken), writeCfg("member.yaml", memberToken), storageRoot
}

func runMemorySyncCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// memoryFilesUnder counts regular files below the storage root — the sweeps
// write under crews/<id>/…/.memory, and "nothing" is the expected count for
// every run in this file.
func memoryFilesUnder(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}

// Both sweeps, both renderings, through the binary. The table names the
// workspace and the candidate; the JSON is the server's own report.
func TestAcceptance_AdminMemorySync_RunsBothSweeps(t *testing.T) {
	ownerCfg, _, root := startMemorySyncAcceptanceServer(t)

	for _, tc := range []struct{ sub, sweep string }{
		{"user-model", "user_model"},
		{"peer-cards", "peer_card"},
	} {
		t.Run(tc.sub, func(t *testing.T) {
			out, err := runMemorySyncCLI(t, ownerCfg, "admin", "memory", "sync", tc.sub)
			if err != nil {
				t.Fatalf("admin memory sync %s: %v\n%s", tc.sub, err, out)
			}
			if !strings.Contains(out, memorySyncAcceptanceWorkspaceID) {
				t.Errorf("table does not name the workspace:\n%s", out)
			}
			if !strings.Contains(out, "CANDIDATES") || !strings.Contains(out, "EMPTY") {
				t.Errorf("table lacks the outcome columns:\n%s", out)
			}

			jsonOut, err := runMemorySyncCLI(t, ownerCfg, "admin", "memory", "sync", tc.sub, "-f", "json")
			if err != nil {
				t.Fatalf("admin memory sync %s -f json: %v\n%s", tc.sub, err, jsonOut)
			}
			var res memorySyncResult
			if err := json.Unmarshal([]byte(jsonOut), &res); err != nil {
				t.Fatalf("json output is not the server report: %v\n%s", err, jsonOut)
			}
			if res.Sweep != tc.sweep {
				t.Errorf("sweep = %q, want %q", res.Sweep, tc.sweep)
			}
			if res.DryRun {
				t.Errorf("a real run reported dry_run=true")
			}
			if len(res.Workspaces) != 1 || res.Workspaces[0].WorkspaceID != memorySyncAcceptanceWorkspaceID {
				t.Fatalf("workspaces = %+v, want exactly the current one", res.Workspaces)
			}
			ws := res.Workspaces[0]
			// The one candidate crossed the threshold and the (unconfigured)
			// extractor had nothing to say: the silent outcome, made visible.
			if ws.Candidates != 1 || ws.SkippedEmpty != 1 || ws.Writes != 0 || ws.Errors != 0 {
				t.Errorf("summary = %+v, want 1 candidate / 1 skipped_empty", ws)
			}
		})
	}
	if n := memoryFilesUnder(t, root); n != 0 {
		t.Errorf("an empty extraction wrote %d file(s) under the storage root", n)
	}
}

// --dry-run is echoed by the server and stated by the table, and --all is
// the instance-wide run the scheduler does (one workspace here, so the same
// row — the point is that the flag is accepted and reaches the server).
func TestAcceptance_AdminMemorySync_DryRunAndAll(t *testing.T) {
	ownerCfg, _, root := startMemorySyncAcceptanceServer(t)

	out, err := runMemorySyncCLI(t, ownerCfg, "admin", "memory", "sync", "user-model", "--dry-run", "--all")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Dry run") {
		t.Errorf("table output does not say it was a dry run:\n%s", out)
	}
	jsonOut, err := runMemorySyncCLI(t, ownerCfg, "admin", "memory", "sync", "user-model", "--dry-run", "--all", "-f", "json")
	if err != nil {
		t.Fatalf("dry run json: %v\n%s", err, jsonOut)
	}
	var res memorySyncResult
	if err := json.Unmarshal([]byte(jsonOut), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, jsonOut)
	}
	if !res.DryRun {
		t.Errorf("server did not echo dry_run: %+v", res)
	}
	if len(res.Workspaces) != 1 {
		t.Errorf("--all over a one-workspace instance reported %d workspaces", len(res.Workspaces))
	}
	if n := memoryFilesUnder(t, root); n != 0 {
		t.Errorf("dry run wrote %d file(s)", n)
	}
}

// A MEMBER gets the same refusal every other /admin/* mutation gives — the
// route is registered through authedMut(roleManage) and the handler checks
// again — and the sweep does not run.
func TestAcceptance_AdminMemorySync_MemberIsRefused(t *testing.T) {
	_, memberCfg, _ := startMemorySyncAcceptanceServer(t)

	for _, sub := range []string{"user-model", "peer-cards"} {
		out, err := runMemorySyncCLI(t, memberCfg, "admin", "memory", "sync", sub)
		if err == nil {
			t.Fatalf("%s: expected a non-zero exit for a MEMBER, got success:\n%s", sub, out)
		}
		if !strings.Contains(strings.ToLower(out), "forbidden") && !strings.Contains(out, "403") {
			t.Errorf("%s: refusal does not read as a 403:\n%s", sub, out)
		}
	}
}

// The CLI's own cap must clear the server's per-candidate budget, or a real
// run on a workspace with a few operators is cancelled — server side too, via
// the request context — before it finishes.
func TestAdminMemorySync_TimeoutClearsTheServerBudget(t *testing.T) {
	const curatorFallbackBudget = 30 * time.Second // internal/usermodel defaultFallbackTimeout
	if memorySyncTimeout <= 4*curatorFallbackBudget {
		t.Fatalf("memorySyncTimeout = %s clears fewer than four extractions at the curator's %s fallback budget",
			memorySyncTimeout, curatorFallbackBudget)
	}
}
