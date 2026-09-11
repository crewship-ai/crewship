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
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process -> real auth/router -> migrated SQLite -> real Git archive.
func TestAcceptance_PageProjectGitHistoryRestore(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cabcdefghijklmnopqrs"
	token := "crewship_cli_pagesproject0000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cabcdefghijklmnopqrs','Pages','pages-cli')`,
		`INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('pages-cli-crew','cabcdefghijklmnopqrs','Operations','ops','free')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,author_crew_id,definition_json,definition_hash,status) VALUES('pages-cli-routine','cabcdefghijklmnopqrs','health-check','Health check','pages-cli-crew','{}','h','active')`,
		`INSERT INTO users(id,email,full_name) VALUES('pages-cli-owner','pages@example.invalid','Pages Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('pages-cli-member','cabcdefghijklmnopqrs','pages-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('pages-cli-token','pages-cli-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	projectsPath := t.TempDir()
	options := []api.RouterOption{api.WithPageProjectsPath(projectsPath)}
	image := os.Getenv("PAGES_TEST_BUILD_IMAGE")
	if image != "" {
		options = append(options, api.WithPageBuildImage(image), api.WithPageRuntime("https://pages.example.net", "https://studio.example.com"))
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(), options...)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
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
		out, err := run(args...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	pageFile := filepath.Join(t.TempDir(), "page.yaml")
	pageYAML := "apiVersion: crewship/v1\nkind: Page\nmetadata:\n  slug: health\n  name: Health\nspec:\n  panels:\n    - id: mysql\n      schema: status.v1\n      owner: crew/ops\n      producer: script/check-mysql.sh\n      sla: 30s\n      actions:\n        - id: refresh\n          kind: call\n          label: Check now\n          routine: health-check\n"
	if err := os.WriteFile(pageFile, []byte(pageYAML), 0600); err != nil {
		t.Fatal(err)
	}
	must("page", "create", "--file", pageFile)
	directory := filepath.Join(t.TempDir(), "application")
	must("page", "project", "init", "--dir", directory)
	source := must("page", "project", "pack", directory)
	p, err := pages.ParseSourceProject(strings.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "source.yaml")
	b, err := p.MarshalYAMLSource()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	saved := must("page", "project", "set", "health", "--file", file, "--revision", "0")
	if !strings.Contains(saved, "git_commit") {
		t.Fatal("CLI lost commit receipt: " + saved)
	}
	must("page", "project", "set", "health", "--file", file, "--revision", "1")
	var integrity struct {
		Healthy bool `json:"healthy"`
	}
	if report := must("page", "project", "fsck", "health"); json.Unmarshal([]byte(report), &integrity) != nil || !integrity.Healthy {
		t.Fatal(report)
	}
	must("page", "project", "compact")
	if _, err := run("page", "project", "compact", "--discard-history"); err == nil {
		t.Fatal("history discard accepted without confirmation")
	}
	history := must("page", "project", "history", "health")
	var decoded struct {
		Revisions []struct {
			Revision int    `json:"revision"`
			Commit   string `json:"git_commit"`
		} `json:"revisions"`
	}
	if err := json.Unmarshal([]byte(history), &decoded); err != nil || len(decoded.Revisions) != 2 {
		t.Fatalf("history %s: %v", history, err)
	}
	if !pages.ValidProjectCommit(decoded.Revisions[0].Commit) {
		t.Fatal(history)
	}
	must("page", "project", "get", "health", "--revision", "1")
	if out, err := run("page", "project", "restore", "health", "--revision", "1", "--expected-revision", "1"); err == nil || !strings.Contains(out, "revision changed") {
		t.Fatalf("stale restore accepted %s %v", out, err)
	}
	must("page", "project", "restore", "health", "--revision", "1", "--expected-revision", "2")
	current := must("page", "project", "get", "health")
	var draft struct {
		Revision int                  `json:"revision"`
		Project  *pages.SourceProject `json:"project"`
	}
	if err := json.Unmarshal([]byte(current), &draft); err != nil || draft.Revision != 3 {
		t.Fatalf("restored draft %s %v", current, err)
	}
	expected, _ := p.Digest()
	actual, _ := draft.Project.Digest()
	if actual != expected {
		t.Fatal("CLI restore lost source bytes")
	}
	if image == "" {
		t.Log("Docker publication path requires PAGES_TEST_BUILD_IMAGE")
		return
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(must("page", "project", "build", "health", "--revision", "3")), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(40 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		var preview struct {
			Build struct {
				State string `json:"state"`
				Error string `json:"error"`
			} `json:"build"`
		}
		if err := json.Unmarshal([]byte(must("page", "project", "preview", "health")), &preview); err != nil {
			t.Fatal(err)
		}
		if preview.Build.State == "ready" {
			ready = true
			break
		}
		if preview.Build.State == "failed" {
			t.Fatal(preview.Build.Error)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("CLI build timeout")
	}
	must("page", "project", "check", "health", "--build", job.ID, "--revision", "3")
	if _, err := run("page", "project", "publish", "health", "--build", job.ID, "--revision", "3", "--expected-publication", "0"); err == nil {
		t.Fatal("publication accepted without code review")
	}
	must("page", "project", "publish", "health", "--build", job.ID, "--revision", "3", "--expected-publication", "0", "--reviewed-code")
	must("page", "project", "publish", "health", "--build", job.ID, "--revision", "3", "--expected-publication", "0", "--reviewed-code")
	must("page", "project", "rollback", "health", "--publication", "1", "--expected-publication", "1", "--reviewed-code")
	var app struct {
		Publication struct {
			Version int `json:"version"`
		} `json:"publication"`
	}
	if err := json.Unmarshal([]byte(must("page", "project", "application", "health")), &app); err != nil || app.Publication.Version != 2 {
		t.Fatalf("CLI rollback failed: %v", err)
	}

	exercisePageRevalidation(t, db, srv.URL, ws)
	exerciseOperationalPageResults(t, must)
	var receipt struct {
		PendingID string `json:"pending_id"`
	}
	dispatch := []string{"page", "action", "health/mysql", "refresh", "--publication", "2", "--idempotency-key", "cli-once"}
	if err := json.Unmarshal([]byte(must(dispatch...)), &receipt); err != nil || receipt.PendingID == "" {
		t.Fatalf("action receipt: %v", err)
	}
	var replay struct {
		PendingID string `json:"pending_id"`
	}
	if err := json.Unmarshal([]byte(must(dispatch...)), &replay); err != nil || replay.PendingID != receipt.PendingID {
		t.Fatalf("action replay: %v", err)
	}
	status := must("page", "project", "action-status", "health", receipt.PendingID)
	if !strings.Contains(status, "pending_status") {
		t.Fatal(status)
	}
	if out, err := run("page", "action", "health/mysql", "refresh", "--publication", "1", "--idempotency-key", "stale-click"); err == nil || !strings.Contains(out, "changed") {
		t.Fatalf("stale action accepted: %s %v", out, err)
	}
	must("page", "project", "publications", "health")
	if _, err := run("page", "project", "unpublish", "health", "--expected-publication", "2"); err == nil {
		t.Fatal("withdrawal accepted without confirmation")
	}
	must("page", "project", "unpublish", "health", "--expected-publication", "2", "--yes")
	must("page", "project", "unpublish", "health", "--expected-publication", "2", "--yes")
	if out := must("page", "project", "application", "health"); !strings.Contains(out, `"publication":null`) && !strings.Contains(out, `"publication": null`) {
		t.Fatal("CLI withdrawal left application live", out)
	}
	srv.Close()
	t.Run("restart", func(t *testing.T) {
		exercisePageDaemonRestart(t, db, binary, projectsPath, ws, token, job.ID)
	})

}
