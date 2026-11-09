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

// A producer starts a run, writes a batch and finishes; a second, read-only
// identity sees the outcome through the same binary without the reason.
func TestAcceptance_DashboardDataRunsBinary(t *testing.T) {
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
	const workspace = "cdashboardrunsworkspacexxx"
	tokens := map[string]string{"owner": "crewship_cli_dashboard_runs_owner", "reader": "crewship_cli_dashboard_runs_reader"}
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('runs-owner','runs-owner@example.test'),('runs-reader','runs-reader@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdashboardrunsworkspacexxx','Runs','dashboard-runs')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('runs-owner-member','cdashboardrunsworkspacexxx','runs-owner','OWNER'),('runs-reader-member','cdashboardrunsworkspacexxx','runs-reader','MEMBER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for who, token := range tokens {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,?,'fixture',?,'STANDARD','["pages:*"]')`, "runs-token-"+who, "runs-"+who, sha256HexToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{4}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "dashboard-runs-cli-synthetic-secret", logger, api.WithJournal(audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	configs := map[string]string{}
	for who, token := range tokens {
		configs[who] = filepath.Join(t.TempDir(), who+".yaml")
		if err := os.WriteFile(configs[who], []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary := buildCrewshipBinary(t)
	run := func(who, input string, args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"page", "artifact"}, args...)...)
		command.Stdin = strings.NewReader(input)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "CREWSHIP_") {
				command.Env = append(command.Env, value)
			}
		}
		command.Env = append(command.Env, "CREWSHIP_CONFIG="+configs[who], "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
		return command.CombinedOutput()
	}
	manifest := `{"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":"<h1 id=artifact-title>Revenue</h1>"},{"path":"dashboard.json","media_type":"application/json","content":"{\"schema_version\":1,\"collections\":[\"sales\"]}"}]}`
	if out, err := run("owner", manifest, "create", "runs-board", "--name", "Runs", "--file", "-"); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	if out, err := run("owner", "", "share", "runs-board", "--base-version", "1", "--version", "1"); err != nil {
		t.Fatalf("share: %v %s", err, out)
	}
	if _, err := db.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) SELECT id,'user','runs-reader','read','runs-owner' FROM pages WHERE workspace_id=? AND slug='runs-board'`, workspace); err != nil {
		t.Fatal(err)
	}
	decode := func(out []byte) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatalf("decode: %v %s", err, out)
		}
		return v
	}
	out, err := run("owner", "", "data", "runs", "start", "runs-board", "sales", "--base-version", "1", "--key", "nightly-1")
	if err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	started := decode(out)
	id, _ := started["id"].(string)
	if started["state"] != "running" || !strings.HasPrefix(id, "run_") || bytes.Contains(out, []byte("runs-owner")) {
		t.Fatalf("started: %s", out)
	}
	if out, err := run("owner", "", "data", "runs", "start", "runs-board", "sales", "--base-version", "1", "--key", "nightly-1"); err != nil || decode(out)["id"] != id {
		t.Fatalf("idempotent start: %v %s", err, out)
	}
	if out, err := run("reader", "", "data", "runs", "list", "runs-board", "--version", "1"); err != nil || !bytes.Contains(out, []byte(`"state": "running"`)) {
		t.Fatalf("reader sees running: %v %s", err, out)
	}
	if out, err := run("owner", `{"base_version":1,"documents":[{"id":"north","ifVersion":0,"value":{"revision":"PRODUCER-REV-42"}}]}`, "data", "batch", "runs-board", "sales", "--file", "-"); err != nil {
		t.Fatalf("batch: %v %s", err, out)
	}
	if out, err := run("owner", "", "data", "runs", "finish", "runs-board", "sales", id, "--base-version", "1", "--state", "succeeded"); err != nil || decode(out)["state"] != "succeeded" {
		t.Fatalf("finish: %v %s", err, out)
	}
	second, err := run("owner", "", "data", "runs", "start", "runs-board", "sales", "--base-version", "1", "--key", "nightly-2")
	if err != nil {
		t.Fatalf("second start: %v %s", err, second)
	}
	if out, err := run("owner", "", "data", "runs", "finish", "runs-board", "sales", decode(second)["id"].(string), "--base-version", "1", "--state", "failed", "--reason", "warehouse 502 password=hunter2"); err != nil || bytes.Contains(out, []byte("hunter2")) {
		t.Fatalf("failed finish: %v %s", err, out)
	}
	if out, err := run("reader", "", "data", "runs", "start", "runs-board", "sales", "--base-version", "1", "--key", "reader"); err == nil {
		t.Fatalf("reader started a run: %s", out)
	}
	out, err = run("reader", "", "data", "runs", "list", "runs-board", "--version", "1")
	if err != nil {
		t.Fatalf("reader list: %v %s", err, out)
	}
	var summary struct {
		Runs []struct {
			Collection    string         `json:"collection"`
			Latest        map[string]any `json:"latest"`
			LastSuccessAt string         `json:"last_success_at"`
		} `json:"runs"`
	}
	if json.Unmarshal(out, &summary) != nil || len(summary.Runs) != 1 || summary.Runs[0].Latest["state"] != "failed" || summary.Runs[0].LastSuccessAt == "" || summary.Runs[0].Latest["failure_reason"] != nil || bytes.Contains(out, []byte("runs-owner")) {
		t.Fatalf("reader summary: %s", out)
	}
	if out, err := run("owner", "", "data", "runs", "list", "runs-board", "--version", "1"); err != nil || !bytes.Contains(out, []byte("warehouse 502")) || bytes.Contains(out, []byte("hunter2")) {
		t.Fatalf("owner summary: %v %s", err, out)
	}
	if out, err := run("reader", "", "data", "get", "runs-board", "sales", "--version", "1"); err != nil || !bytes.Contains(out, []byte("PRODUCER-REV-42")) {
		t.Fatalf("last good data kept after failure: %v %s", err, out)
	}
}
