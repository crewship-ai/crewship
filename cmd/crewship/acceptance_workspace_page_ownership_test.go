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

// A real CLI process reaches the authenticated Router and migrated SQLite.
// Synthetic extension rows exercise custody without requiring output delivery.
func TestAcceptance_WorkspacePageOwnership(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const workspace = "ccustodyworkspace001"
	const token = "crewship_cli_custodyowner000000000000000"
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ccustodyworkspace001','Custody','custody-cli')`,
		`INSERT INTO users(id,email) VALUES('custody-admin','admin@example.invalid'),('custody-old','old@example.invalid'),('custody-reader','reader@example.invalid'),('custody-ungranted','ungranted@example.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('custody-m1','ccustodyworkspace001','custody-admin','OWNER'),('custody-m2','ccustodyworkspace001','custody-old','MEMBER'),('custody-m3','ccustodyworkspace001','custody-reader','MEMBER'),('custody-m4','ccustodyworkspace001','custody-ungranted','MEMBER')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('custody-page','ccustodyworkspace001','private-output','Private output','custody-old','{}')`,
		`CREATE TABLE documents(page_id TEXT PRIMARY KEY REFERENCES pages(id),private_content TEXT)`,
		`INSERT INTO documents VALUES('custody-page','NEVER_EXPOSE_THIS')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('custody-page','user','custody-reader','read','custody-old')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('custody-cli-token','custody-admin','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
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
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "CREWSHIP_CONFIG="+config, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		output, err := command.CombinedOutput()
		return string(output), err
	}
	output, err := run("workspace", "page-ownership", "list", "--owner", "custody-old")
	if err != nil {
		t.Fatalf("custody list: %v\n%s", err, output)
	}
	var listing struct {
		Pages []struct {
			ID       string `json:"id"`
			Eligible []struct {
				User string `json:"user_id"`
			} `json:"eligible_successors"`
		} `json:"pages"`
	}
	if err := json.Unmarshal([]byte(output), &listing); err != nil || len(listing.Pages) != 1 || listing.Pages[0].ID != "custody-page" {
		t.Fatalf("listing %s %v", output, err)
	}
	if strings.Contains(output, "NEVER_EXPOSE_THIS") {
		t.Fatal("CLI exposed content")
	}
	eligible := map[string]bool{}
	for _, candidate := range listing.Pages[0].Eligible {
		eligible[candidate.User] = true
	}
	if !eligible["custody-reader"] || eligible["custody-ungranted"] {
		t.Fatalf("eligible successors %v", eligible)
	}
	transfer := func(successor string) (string, error) {
		return run("workspace", "page-ownership", "transfer", "custody-page", "--expected-owner", "custody-old", "--successor", successor)
	}
	if output, err := transfer("custody-ungranted"); err == nil || !strings.Contains(output, "current page read access") {
		t.Fatalf("ungranted successor: %v %s", err, output)
	}
	if output, err := transfer("custody-reader"); err != nil || !strings.Contains(output, `"owner_user_id": "custody-reader"`) {
		t.Fatalf("transfer: %v %s", err, output)
	}
	if output, err := transfer("custody-admin"); err == nil || !strings.Contains(output, "ownership changed") {
		t.Fatalf("stale owner: %v %s", err, output)
	}
	var content, issuer string
	if err := db.QueryRow(`SELECT private_content FROM documents WHERE page_id='custody-page'`).Scan(&content); err != nil || content != "NEVER_EXPOSE_THIS" {
		t.Fatalf("retained source %s %v", content, err)
	}
	if err := db.QueryRow(`SELECT granted_by_user_id FROM page_grants WHERE page_id='custody-page'`).Scan(&issuer); err != nil || issuer != "custody-old" {
		t.Fatalf("retained grant issuer %s %v", issuer, err)
	}
}
