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

func TestAcceptance_WorkspaceArtifactPolicy(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "cartifactpolicy000000001"
	const token = "crewship_cli_artifactpolicy000000000000000"
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cartifactpolicy000000001','Policy','artifact-policy-cli')`,
		`INSERT INTO users(id,email) VALUES('policy-admin','admin@example.invalid'),('policy-page-owner','owner@example.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('policy-admin-member','cartifactpolicy000000001','policy-admin','OWNER'),('policy-owner-member','cartifactpolicy000000001','policy-page-owner','MEMBER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('policy-crew','cartifactpolicy000000001','Policy crew','policy-crew')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('policy-existing-page','cartifactpolicy000000001','existing','Existing','policy-page-owner','{}')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at,scopes) VALUES('policy-cli-token','policy-admin','test',?,datetime('now'),'["workspace:admin"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	config := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(config, []byte("server: "+server.URL+"\nworkspace: "+workspace+"\ntoken: "+token+"\nformat: json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(args ...string) (string, error) {
		command := exec.Command(binary, append([]string{"workspace", "artifact-policy"}, args...)...)
		command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		output, err := command.CombinedOutput()
		return string(output), err
	}
	var policy struct {
		Enabled bool     `json:"enabled"`
		CrewIDs []string `json:"crew_ids"`
		Version int64    `json:"version"`
	}
	read := func() {
		t.Helper()
		output, err := run("show")
		if err != nil || json.Unmarshal([]byte(output), &policy) != nil {
			t.Fatalf("show: %v %s", err, output)
		}
	}
	read()
	if policy.Enabled || policy.Version != 0 || policy.CrewIDs == nil || len(policy.CrewIDs) != 0 {
		t.Fatalf("default policy %+v", policy)
	}
	if output, err := run("set", "--base-version", "0", "--enabled=true", "--crew-id", "policy-crew"); err != nil {
		t.Fatalf("enable: %v %s", err, output)
	}
	read()
	if !policy.Enabled || policy.Version != 1 || len(policy.CrewIDs) != 1 || policy.CrewIDs[0] != "policy-crew" {
		t.Fatalf("enabled policy %+v", policy)
	}
	if output, err := run("set", "--base-version", "0", "--enabled=false", "--clear-crews"); err == nil || !strings.Contains(output, "artifact creation policy changed") {
		t.Fatalf("stale CAS: %v %s", err, output)
	}
	read()
	if !policy.Enabled || policy.Version != 1 {
		t.Fatalf("stale update changed policy %+v", policy)
	}
	var owner string
	if err := db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='policy-existing-page'`).Scan(&owner); err != nil || owner != "policy-page-owner" {
		t.Fatalf("policy changed existing Page owner %s: %v", owner, err)
	}
	if output, err := run("set", "--base-version", "1", "--enabled=false", "--clear-crews"); err != nil {
		t.Fatalf("disable: %v %s", err, output)
	}
	read()
	if policy.Enabled || policy.Version != 2 || len(policy.CrewIDs) != 0 {
		t.Fatalf("disabled policy %+v", policy)
	}
	if _, err := db.Exec(`UPDATE cli_tokens SET scopes='["pages:read"]' WHERE id='policy-cli-token'`); err != nil {
		t.Fatal(err)
	}
	if output, err := run("show"); err == nil || !strings.Contains(output, "current CLI scope required") {
		t.Fatalf("narrow CLI scope read policy: %v %s", err, output)
	}
}
