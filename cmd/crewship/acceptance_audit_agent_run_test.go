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

// #5317 PR4: an agent run's artifact access is listed by `crewship audit`
// under the human the agent acted for, with the agent, run and reach in its
// metadata. The row shape is the one the server writes
// (recordAgentRunArtifactAccess); the server side is covered in internal/api.
func TestAcceptance_AuditShowsTheHumanBehindAnAgentRun(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	token := "crewship_cli_auditagentrun0000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('ar-ws','Runs','runs','2026-01-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('ar-admin','admin@runs.invalid','Admin'),('ar-human','human@runs.invalid','Human')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('ar-m1','ar-ws','ar-admin','OWNER'),('ar-m2','ar-ws','ar-human','MEMBER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('ar-page','ar-ws','report','Report','ar-human','{}')`,
		`INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES('agent-run-read-x','ar-ws','ar-human','artifact.agent_run_accessed','page','ar-page','{"agent_id":"ar-agent","crew_id":"ar-crew","run_id":"ar-run","chat_id":"ar-chat","reach":"referenced","operation":"read"}','2026-11-14T03:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('ar-token','ar-admin','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
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
	if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: runs\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return string(out)
	}
	table := run("audit", "--action", "artifact.agent_run_accessed")
	if !strings.Contains(table, "human@runs.invalid") || !strings.Contains(table, "artifact.agent_run_accessed") {
		t.Fatalf("table does not name the human:\n%s", table)
	}
	var listed []struct {
		UserEmail string `json:"user_email"`
		Metadata  string `json:"metadata"`
	}
	raw := run("audit", "--action", "artifact.agent_run_accessed", "-f", "json")
	if err := json.Unmarshal([]byte(raw), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("json: %v %s", err, raw)
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(listed[0].Metadata), &meta); err != nil || meta["agent_id"] != "ar-agent" || meta["run_id"] != "ar-run" || meta["reach"] != "referenced" || listed[0].UserEmail != "human@runs.invalid" {
		t.Fatalf("json row %+v meta %v %v", listed[0], meta, err)
	}
}
