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

// The binary reads aggregate storage as an owner, is refused as a member, and
// a save into a full workspace exits with artifact_storage_quota while every
// confirmed version stays readable.
func TestAcceptance_ArtifactStorageBinary(t *testing.T) {
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
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
	const workspace = "cstoragecliworkspacexxxxxx"
	const ownerToken = "crewship_cli_storage_owner_fixture"
	const memberToken = "crewship_cli_storage_member_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('storage-cli-owner','storage-cli-owner@example.test')`,
		`INSERT INTO users(id,email) VALUES('storage-cli-member','storage-cli-member@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cstoragecliworkspacexxxxxx','Storage','storage-cli')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('storage-cli-m1','cstoragecliworkspacexxxxxx','storage-cli-owner','OWNER')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('storage-cli-m2','cstoragecliworkspacexxxxxx','storage-cli-member','MEMBER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for id, token := range map[string]string{"storage-cli-owner": ownerToken, "storage-cli-member": memberToken} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES(?,?,'fixture',?,'STANDARD','["pages:*"]')`, id+"-token", id, sha256HexToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{5}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "artifact-storage-cli-synthetic-secret", logger, api.WithJournal(audit))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	configs := map[string]string{}
	for name, token := range map[string]string{"owner": ownerToken, "member": memberToken} {
		config := filepath.Join(t.TempDir(), name+".yaml")
		if err := os.WriteFile(config, []byte(fmt.Sprintf("server: %s\ntoken: %s\nworkspace: %s\nformat: json\n", server.URL, token, workspace)), 0600); err != nil {
			t.Fatal(err)
		}
		configs[name] = config
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
	manifest := `{"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":"<h1 id=title>One</h1>"}]}`
	if out, err := run("owner", manifest, "create", "storage-demo", "--name", "Storage", "--file", "-"); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	out, err := run("owner", "", "storage")
	if err != nil {
		t.Fatalf("owner storage: %v %s", err, out)
	}
	var usage struct {
		UsedBytes  int64  `json:"used_bytes"`
		LimitBytes int64  `json:"limit_bytes"`
		Level      string `json:"level"`
		Retention  string `json:"retention"`
	}
	if err := json.Unmarshal(out, &usage); err != nil || usage.UsedBytes <= 0 || usage.LimitBytes <= usage.UsedBytes || usage.Level != "none" || usage.Retention != "keep-all" {
		t.Fatalf("owner usage %s %v", out, err)
	}
	if out, err := run("member", "", "storage"); err == nil || !strings.Contains(string(out), "403") || strings.Contains(string(out), "used_bytes") {
		t.Fatalf("member read workspace usage: %v %s", err, out)
	}
	if _, err := db.Exec(`UPDATE artifact_storage_workspaces SET max_bytes=used_bytes WHERE workspace_id=?`, workspace); err != nil {
		t.Fatal(err)
	}
	next := `{"entrypoint":"index.html","files":[{"path":"index.html","media_type":"text/html","content":"<h1 id=title>Two</h1>"}]}`
	out, err = run("owner", next, "publish", "storage-demo", "--base-version", "1", "--file", "-")
	if err == nil || !strings.Contains(string(out), "artifact_storage_quota") || !strings.Contains(string(out), "nothing was saved") {
		t.Fatalf("full workspace publish: %v %s", err, out)
	}
	var versions int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_versions v JOIN pages p ON p.id=v.page_id WHERE p.slug='storage-demo'`).Scan(&versions); err != nil || versions != 1 {
		t.Fatalf("refusal changed history: %d %v", versions, err)
	}
}
