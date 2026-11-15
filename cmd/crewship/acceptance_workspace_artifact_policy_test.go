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
		Limits  struct {
			ChatRunCreate  int `json:"chat_run_create_limit"`
			RunRead        int `json:"run_read_limit"`
			CrewHourlyPage int `json:"crew_hourly_page_limit"`
		} `json:"limits"`
		SaveRequiresRunToken *bool `json:"save_requires_run_token"`
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
	// The workspace chosen by slug, as every other command accepts it, must
	// reach the same policy: the slug is resolved to the ID before the path
	// is built, so it is not sent raw into /workspaces/{id}/.
	output, err := run("show", "--workspace", "artifact-policy-cli")
	if err != nil || json.Unmarshal([]byte(output), &policy) != nil || policy.Version != 2 {
		t.Fatalf("show by slug: %v %s", err, output)
	}
	if output, err := run("set", "-w", "artifact-policy-cli", "--base-version", "2", "--enabled=true", "--crew-id", "policy-crew"); err != nil {
		t.Fatalf("set by slug: %v %s", err, output)
	}
	read()
	if !policy.Enabled || policy.Version != 3 || len(policy.CrewIDs) != 1 {
		t.Fatalf("policy set by slug %+v", policy)
	}
	if output, err := run("show", "--workspace", "no-such-workspace"); err == nil || !strings.Contains(output, "no-such-workspace") {
		t.Fatalf("unknown slug must fail naming it: %v %s", err, output)
	}
	// #5317 PR2: limits travel through the same command; omitted ones keep
	// their value, out-of-range ones are refused before anything is saved.
	if policy.Limits.ChatRunCreate != 3 || policy.Limits.RunRead != 50 || policy.Limits.CrewHourlyPage != 20 {
		t.Fatalf("default limits %+v", policy.Limits)
	}
	if output, err := run("set", "--base-version", "3", "--enabled=true", "--crew-id", "policy-crew", "--chat-run-create-limit", "5", "--crew-hourly-page-limit", "7"); err != nil {
		t.Fatalf("set limits: %v %s", err, output)
	}
	read()
	if policy.Version != 4 || policy.Limits.ChatRunCreate != 5 || policy.Limits.RunRead != 50 || policy.Limits.CrewHourlyPage != 7 {
		t.Fatalf("limits after set %+v", policy)
	}
	if output, err := run("set", "--base-version", "4", "--enabled=true", "--crew-id", "policy-crew", "--run-read-limit", "501"); err == nil || !strings.Contains(output, "limits out of range") {
		t.Fatalf("out-of-range limit: %v %s", err, output)
	}
	read()
	if policy.Version != 4 || policy.Limits.RunRead != 50 {
		t.Fatalf("refused limit changed policy %+v", policy)
	}
	// #5336: the run-token requirement on the sidecar save path is an
	// explicit opt-in, false until set, kept when the flag is omitted.
	if policy.SaveRequiresRunToken == nil || *policy.SaveRequiresRunToken {
		t.Fatalf("default save_requires_run_token must be reported false: %+v", policy)
	}
	if output, err := run("set", "--base-version", "4", "--enabled=true", "--crew-id", "policy-crew", "--save-requires-run-token=true"); err != nil {
		t.Fatalf("require run token: %v %s", err, output)
	}
	read()
	if policy.Version != 5 || policy.SaveRequiresRunToken == nil || !*policy.SaveRequiresRunToken {
		t.Fatalf("save_requires_run_token after set %+v", policy)
	}
	if output, err := run("set", "--base-version", "5", "--enabled=true", "--crew-id", "policy-crew", "--chat-run-create-limit", "4"); err != nil {
		t.Fatalf("set without the flag: %v %s", err, output)
	}
	read()
	if policy.Version != 6 || !*policy.SaveRequiresRunToken || policy.Limits.ChatRunCreate != 4 {
		t.Fatalf("omitted --save-requires-run-token must keep the value %+v", policy)
	}
	if output, err := run("set", "--base-version", "6", "--enabled=true", "--crew-id", "policy-crew", "--save-requires-run-token=false"); err != nil {
		t.Fatalf("clear run token requirement: %v %s", err, output)
	}
	read()
	if policy.Version != 7 || *policy.SaveRequiresRunToken {
		t.Fatalf("save_requires_run_token after clear %+v", policy)
	}
	if _, err := db.Exec(`UPDATE cli_tokens SET scopes='["pages:read"]' WHERE id='policy-cli-token'`); err != nil {
		t.Fatal(err)
	}
	if output, err := run("show"); err == nil || !strings.Contains(output, "current CLI scope required") {
		t.Fatalf("narrow CLI scope read policy: %v %s", err, output)
	}
}
