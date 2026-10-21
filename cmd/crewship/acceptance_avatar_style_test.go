package main

// #3789 acceptance — the avatar-style flags accept DiceBear 10's Voxel Bot and
// Voxel Art, driven through the BUILT BINARY against the REAL api router over
// a migrated database, and refuse a slug the web app cannot draw before any
// request is sent.

import (
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

const avatarStyleWorkspaceID = "cavatarws0000000000a1"

type avatarStyleEnv struct {
	cfg   string
	style func(agentID string) string
}

func startAvatarStyleServer(t *testing.T) avatarStyleEnv {
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
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Avatars', 'avatars-ws')`, avatarStyleWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('av-owner', 'owner@ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('avm-owner', ?, 'av-owner', 'OWNER')`, avatarStyleWorkspaceID)
	const token = "crewship_cli_avowner00000000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-av', 'av-owner', 't', ?, datetime('now'))`, sha256HexToken(token))
	mustExec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, container_memory_mb, container_cpus, created_at)
		VALUES ('crew-av', ?, 'Avatars', 'avatars', 'free', 4096, 2.0, '2026-01-01T00:00:00Z')`, avatarStyleWorkspaceID)
	mustExec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, tool_profile, timeout_seconds, memory_enabled, created_at)
		VALUES ('agent-av', ?, 'crew-av', 'Existing', 'existing', 'AGENT', 'IDLE', 'CLAUDE_CODE', 'CODING', 1800, 0, '2026-01-01T00:00:00Z')`, avatarStyleWorkspaceID)

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + avatarStyleWorkspaceID + "\ntoken: " + token + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return avatarStyleEnv{cfg: cfgPath, style: func(where string) string {
		t.Helper()
		var s *string
		if err := db.QueryRow(`SELECT avatar_style FROM agents WHERE ` + where).Scan(&s); err != nil {
			t.Fatalf("read avatar_style where %s: %v", where, err)
		}
		if s == nil {
			return "<null>"
		}
		return *s
	}}
}

func runAvatarStyleCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath, "NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAcceptance_AvatarStyle_VoxelSlugs(t *testing.T) {
	env := startAvatarStyleServer(t)

	out, err := runAvatarStyleCLI(t, env.cfg, "agent", "create", "--name", "Voxel", "--slug", "voxel", "--crew", "avatars", "--avatar-style", "voxel-bot")
	if err != nil {
		t.Fatalf("agent create --avatar-style voxel-bot: %v\n%s", err, out)
	}
	if got := env.style("slug = 'voxel'"); got != "voxel-bot" {
		t.Errorf("created agent avatar_style = %q, want voxel-bot", got)
	}

	out, err = runAvatarStyleCLI(t, env.cfg, "agent", "update", "existing", "--avatar-style", "voxel-art")
	if err != nil {
		t.Fatalf("agent update --avatar-style voxel-art: %v\n%s", err, out)
	}
	if got := env.style("id = 'agent-av'"); got != "voxel-art" {
		t.Errorf("updated agent avatar_style = %q, want voxel-art", got)
	}

	out, err = runAvatarStyleCLI(t, env.cfg, "crew", "apply-avatar-style", "avatars", "--style", "voxel-bot")
	if err != nil {
		t.Fatalf("crew apply-avatar-style --style voxel-bot: %v\n%s", err, out)
	}
	if got := env.style("id = 'agent-av'"); got != "voxel-bot" {
		t.Errorf("after apply-avatar-style, avatar_style = %q, want voxel-bot", got)
	}
}

func TestAcceptance_AvatarStyle_RefusesUnknownSlug(t *testing.T) {
	env := startAvatarStyleServer(t)
	for _, args := range [][]string{
		{"agent", "create", "--name", "Robot", "--slug", "robot", "--avatar-style", "robots"},
		{"agent", "update", "existing", "--avatar-style", "Voxel Bot"},
		{"crew", "apply-avatar-style", "avatars", "--style", "blobs"},
	} {
		out, err := runAvatarStyleCLI(t, env.cfg, args...)
		if err == nil {
			t.Errorf("%v succeeded, want a refusal:\n%s", args, out)
			continue
		}
		if !strings.Contains(out, "unknown avatar style") || !strings.Contains(out, "voxel-bot") {
			t.Errorf("%v: the refusal should name the problem and list the slugs, got:\n%s", args, out)
		}
	}
	if got := env.style("id = 'agent-av'"); got != "<null>" {
		t.Errorf("a refused update still wrote avatar_style %q", got)
	}
}

func TestAcceptance_AvatarStyle_HelpListsVoxel(t *testing.T) {
	env := startAvatarStyleServer(t)
	for _, args := range [][]string{
		{"agent", "create", "--help"},
		{"agent", "update", "--help"},
		{"crew", "apply-avatar-style", "--help"},
	} {
		out, err := runAvatarStyleCLI(t, env.cfg, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if !strings.Contains(out, "voxel-bot") || !strings.Contains(out, "voxel-art") {
			t.Errorf("%v help does not list the voxel styles:\n%s", args, out)
		}
	}
}
