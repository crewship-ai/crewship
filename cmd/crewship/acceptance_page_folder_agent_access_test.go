package main

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

// #5317 D2 through the binary: `page folder agent-access` reads the fence,
// sets none/read/propose, refuses edit, needs the flag for anything but none,
// and `page folder acl` reports the level.
func TestAcceptance_PageFolderAgentAccess(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cfolderagentsws000001"
	token := "crewship_cli_folderagents000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cfolderagentsws000001','Folder agents','folder-agents-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('folder-agents-crew','cfolderagentsws000001','Operations','ops','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('folder-agents-owner','folder-agents@example.invalid','Folder Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('folder-agents-member','cfolderagentsws000001','folder-agents-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('folder-agents-token','folder-agents-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	level := func() (string, int64) {
		t.Helper()
		var doc struct {
			AgentAccess string `json:"agent_access"`
			ACLVersion  int64  `json:"acl_version"`
		}
		if out := must("page", "folder", "acl", "ops-board"); json.Unmarshal([]byte(out), &doc) != nil {
			t.Fatalf("acl: %s", out)
		}
		return doc.AgentAccess, doc.ACLVersion
	}
	must("page", "folder", "create", "ops-board", "--name", "Ops board", "--owner", "crew/ops")
	if got, _ := level(); got != "none" {
		t.Fatalf("default level %q", got)
	}
	if out, err := run("page", "folder", "agent-access", "ops-board", "--level", "read"); err == nil || !strings.Contains(out, "artifact_agent_access_modes") {
		t.Fatalf("flag off accepted read: %v %s", err, out)
	}
	if _, err := db.Exec(`INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('folder-agents-flag','ff_artifact_agent_access_modes',?,1)`, ws); err != nil {
		t.Fatal(err)
	}
	if out, err := run("page", "folder", "agent-access", "ops-board", "--level", "edit"); err == nil || !strings.Contains(out, "never gives its agents edit") {
		t.Fatalf("edit level: %v %s", err, out)
	}
	_, before := level()
	must("page", "folder", "agent-access", "ops-board", "--level", "propose")
	if got, after := level(); got != "propose" || after != before+1 {
		t.Fatalf("after set: %q %d (was %d)", got, after, before)
	}
	// #5317 step 5.2: the server names the kinds where propose is a real
	// proposal (the dialog offers Can suggest from it), and the table says so.
	var kinds struct {
		Kinds []string `json:"agent_propose_kinds"`
	}
	if out := must("page", "folder", "acl", "ops-board"); json.Unmarshal([]byte(out), &kinds) != nil || strings.Join(kinds.Kinds, ",") != "docs" {
		t.Fatalf("agent_propose_kinds: %s", out)
	}
	if out := must("page", "folder", "acl", "ops-board", "-f", "table"); !strings.Contains(out, "propose (suggestions on docs, read on other kinds") {
		t.Fatalf("table output: %s", out)
	}
	must("page", "folder", "agent-access", "ops-board", "--level", "none")
	if got, _ := level(); got != "none" {
		t.Fatalf("after reset: %q", got)
	}
}
