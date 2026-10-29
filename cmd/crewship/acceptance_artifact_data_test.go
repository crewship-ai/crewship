package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestAcceptance_DashboardDataBinary(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	db := opened.DB
	const workspace = "cdashboardcliworkspacexxxx"
	const token = "crewship_cli_dashboard_binary_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('dashboard-cli-owner','dashboard-cli-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdashboardcliworkspacexxxx','Dashboard','dashboard-cli')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('dashboard-cli-member','cdashboardcliworkspacexxxx','dashboard-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('dashboard-cli-token','dashboard-cli-owner','fixture',?,'STANDARD','["pages:*"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{3}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "dashboard-data-cli-synthetic-secret", logger, api.WithJournal(audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	config := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
		t.Fatal(err)
	}
	binary := buildCrewshipBinary(t)
	run := func(input string, args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"page", "artifact"}, args...)...)
		command.Stdin = strings.NewReader(input)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return command.CombinedOutput()
	}
	manifest := `{"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":"<h1 id=artifact-title>Revenue</h1>"},{"path":"dashboard.json","media_type":"application/json","content":"{\"schema_version\":1,\"collections\":[\"sales\"]}"}]}`
	if out, err := run(manifest, "create", "dashboard", "--name", "Dashboard", "--file", "-"); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	batch := `{"base_version":1,"documents":[{"id":"north","ifVersion":0,"value":{"amount":9007199254740993,"label":"` + strings.Repeat("<", 200<<10) + `"}}]}`
	if out, err := run(batch, "data", "batch", "dashboard", "sales", "--file", "-"); err != nil || !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatalf("batch: %v %s", err, out)
	}
	if out, err := run(batch, "data", "batch", "dashboard", "sales", "--file", "-"); err == nil {
		t.Fatalf("stale CAS accepted: %s", out)
	}
	out, err := run("", "data", "get", "dashboard", "sales", "--version", "1")
	if err != nil || !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatalf("read: %v %s", err, out)
	}
	var data struct {
		CodeVersion int64 `json:"code_version"`
		Documents   []struct {
			Version int64 `json:"version"`
		} `json:"documents"`
	}
	if json.Unmarshal(out, &data) != nil || data.CodeVersion != 1 || len(data.Documents) != 1 || data.Documents[0].Version != 1 {
		t.Fatalf("data/code versions changed: %s", out)
	}
	if out, err := run("", "data", "collections", "dashboard", "--version", "1"); err != nil || !bytes.Contains(out, []byte("sales")) {
		t.Fatalf("list: %v %s", err, out)
	}
	var latest int64
	if err := db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE workspace_id=?`, workspace).Scan(&latest); err != nil || latest != 1 {
		t.Fatalf("data published a code version: %d %v", latest, err)
	}
}
