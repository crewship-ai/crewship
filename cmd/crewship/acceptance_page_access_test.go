package main

// acceptance_page_access_test.go — `crewship page access` against a real
// server (pages-collections-access-analysis §4, §5/10, §6; #2528).
//
// A real CLI process → the real router → a migrated SQLite. The endpoint's
// own contract is proved in internal/api/pages_access_test.go; this file
// proves what only the binary can — that the command reaches the route with
// the parameters the handler reads, prints the server's paths verbatim in
// both the table and the machine format, and carries a refusal out as a
// non-zero exit with the server's sentence rather than a decoded envelope.

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

func TestAcceptance_PageAccessListsSubjectsAndPages(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "caccessworkspace0001"
	ownerToken := "crewship_cli_pagesaccessowner00000000000"
	bobToken := "crewship_cli_pagesaccessbob0000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('caccessworkspace0001','Access','access-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('access-cli-ops','caccessworkspace0001','Operations','ops','free')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('access-cli-lookout','caccessworkspace0001','Lookout','lookout','free')`,
		`INSERT INTO users(id,email,full_name) VALUES('access-cli-owner','owner@example.invalid','Page Owner')`,
		`INSERT INTO users(id,email,full_name) VALUES('access-cli-bob','bob@example.invalid','Bob')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('access-cli-m1','caccessworkspace0001','access-cli-owner','OWNER')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('access-cli-m2','caccessworkspace0001','access-cli-bob','MEMBER')`,
		`INSERT INTO crew_members(id,crew_id,user_id,role) VALUES('access-cli-cm1','access-cli-ops','access-cli-bob','MEMBER')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug) VALUES('access-cli-agent','access-cli-ops','caccessworkspace0001','Watcher','watcher')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, tok := range []struct{ id, user, token string }{
		{"access-cli-token-owner", "access-cli-owner", ownerToken},
		{"access-cli-token-bob", "access-cli-bob", bobToken},
	} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`,
			tok.id, tok.user, sha256HexToken(tok.token)); err != nil {
			t.Fatal(err)
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()

	configFor := func(token, format string) string {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+token+"\nformat: "+format+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	binary := buildCrewshipBinary(t)
	runAs := func(cfg string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	ownerJSON := configFor(ownerToken, "json")
	ownerTable := configFor(ownerToken, "table")
	bobTable := configFor(bobToken, "table")
	must := func(cfg string, args ...string) string {
		out, err := runAs(cfg, args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}

	pageFile := filepath.Join(t.TempDir(), "page.yaml")
	pageYAML := "apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: health\n  name: Health\nspec:\n  panels:\n" +
		"    - id: mysql\n      schema: status.v1\n      owner: crew/ops\n      producer: script/check-mysql.sh\n      sla: 30s\n" +
		"    - id: radar\n      schema: status.v1\n      owner: crew/lookout\n      producer: script/radar.sh\n      sla: 30s\n"
	if err := os.WriteFile(pageFile, []byte(pageYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	must(ownerJSON, "page", "create", "--file", pageFile)
	must(ownerJSON, "page", "grant", "health", "--user", "bob@example.invalid", "--level", "read")
	must(ownerJSON, "page", "grant", "health", "--agent", "watcher", "--level", "produce", "--panels", "mysql")

	// ── who reaches the page, as JSON ──────────────────────────────────────
	var access struct {
		Page     string `json:"page"`
		Subjects []struct {
			SubjectType string   `json:"subject_type"`
			Label       string   `json:"label"`
			Paths       []string `json:"paths"`
		} `json:"subjects"`
	}
	out := must(ownerJSON, "page", "access", "health")
	if err := json.Unmarshal([]byte(out), &access); err != nil {
		t.Fatalf("page access did not print the server's JSON: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, s := range access.Subjects {
		got[s.SubjectType+"/"+s.Label] = strings.Join(s.Paths, ",")
	}
	want := map[string]string{
		"user/owner@example.invalid": "owner,role",
		"user/bob@example.invalid":   "panel_crew:ops,grant:page:read",
		"crew/ops":                   "panel_crew:ops",
		"crew/lookout":               "panel_crew:lookout",
		"agent/watcher":              "grant:page:produce",
	}
	for subject, paths := range want {
		if got[subject] != paths {
			t.Errorf("%s: paths = %q, want %q\n%s", subject, got[subject], paths, out)
		}
	}
	if len(got) != len(want) {
		t.Errorf("subjects = %v, want exactly %v", got, want)
	}

	// ── the same, as a table ───────────────────────────────────────────────
	table := must(ownerTable, "page", "access", "health")
	for _, cell := range []string{"SUBJECT", "KIND", "PATHS", "bob@example.invalid", "panel_crew:ops, grant:page:read", "agent", "grant:page:produce"} {
		if !strings.Contains(table, cell) {
			t.Errorf("the table does not show %q:\n%s", cell, table)
		}
	}

	// ── what one subject reaches ───────────────────────────────────────────
	var reach struct {
		Subject struct {
			Label string `json:"label"`
		} `json:"subject"`
		Pages []struct {
			Slug  string   `json:"slug"`
			Paths []string `json:"paths"`
		} `json:"pages"`
	}
	out = must(ownerJSON, "page", "access", "--subject", "user:bob@example.invalid")
	if err := json.Unmarshal([]byte(out), &reach); err != nil {
		t.Fatalf("page access --subject did not print the server's JSON: %v\n%s", err, out)
	}
	if reach.Subject.Label != "bob@example.invalid" || len(reach.Pages) != 1 || reach.Pages[0].Slug != "health" ||
		strings.Join(reach.Pages[0].Paths, ",") != "panel_crew:ops,grant:page:read" {
		t.Errorf("bob's reach = %s", out)
	}
	table = must(ownerTable, "page", "access", "--subject", "crew:ops")
	if !strings.Contains(table, "PAGE") || !strings.Contains(table, "health") || !strings.Contains(table, "panel_crew:ops") {
		t.Errorf("crew/ops's reach table:\n%s", table)
	}

	// ── a member may ask about themselves and nobody else ──────────────────
	self := must(bobTable, "page", "access", "--subject", "user:bob@example.invalid")
	if !strings.Contains(self, "grant:page:read") {
		t.Errorf("bob asking about himself:\n%s", self)
	}
	if out, err := runAs(bobTable, "page", "access", "--subject", "user:owner@example.invalid"); err == nil {
		t.Errorf("bob was told what the owner reaches:\n%s", out)
	} else if !strings.Contains(out, "only a workspace admin") {
		t.Errorf("the refusal is not the server's sentence: %v\n%s", err, out)
	}

	// ── a grantee is refused the page's access, with the server's words ────
	if out, err := runAs(bobTable, "page", "access", "health"); err == nil {
		t.Errorf("a read grantee was shown who reaches the page:\n%s", out)
	} else if !strings.Contains(out, "page owner or a workspace admin") {
		t.Errorf("the refusal is not the server's sentence: %v\n%s", err, out)
	}

	// ── malformed invocations are refused before any request ───────────────
	for _, args := range [][]string{
		{"page", "access"},
		{"page", "access", "health", "--subject", "user:bob@example.invalid"},
		{"page", "access", "--subject", "bob@example.invalid"},
	} {
		if out, err := runAs(ownerTable, args...); err == nil {
			t.Errorf("%v was accepted:\n%s", args, out)
		}
	}
}
