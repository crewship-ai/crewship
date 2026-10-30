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

// A Design System artifact is a project artifact whose manifest holds
// design-system.json. Agents create, publish and restore it through the
// existing `page artifact` commands; history comes from the VersionStore.
func TestAcceptance_DesignSystemArtifactBinary(t *testing.T) {
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
	const workspace = "cdesignsystemcliworkspacex"
	const token = "crewship_cli_design_system_binary_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('design-system-cli-owner','design-system-cli-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('cdesignsystemcliworkspacex','Design System','design-system-cli')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('design-system-cli-member','cdesignsystemcliworkspacex','design-system-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('design-system-cli-token','design-system-cli-owner','fixture',?,'STANDARD','["pages:*"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{5}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "design-system-cli-synthetic-secret", logger, api.WithJournal(audit))
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
	manifest := func(primary string) string {
		tokens := `{"schema_version":1,"name":"Harbor","colors":{"primary":"` + primary + `"},"typography":{"body":{"size":16,"weight":400}},"spacing":{"md":16},"radii":{"sm":4}}`
		body, err := json.Marshal(map[string]any{"entrypoint": "index.html", "files": []map[string]string{
			{"path": "index.html", "media_type": "text/html", "content": "<h1 id=title>Harbor tokens</h1>"},
			{"path": "design-system.json", "media_type": "application/json", "content": tokens},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	tokensAt := func(version string) string {
		t.Helper()
		out, err := run("", "get", "harbor", "--version", version)
		if err != nil {
			t.Fatalf("get v%s: %v %s", version, err, out)
		}
		var snapshot struct {
			Manifest struct {
				Files []struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				} `json:"files"`
			} `json:"manifest"`
		}
		if err := json.Unmarshal(out, &snapshot); err != nil {
			t.Fatalf("decode v%s: %v %s", version, err, out)
		}
		for _, file := range snapshot.Manifest.Files {
			if file.Path == "design-system.json" {
				return file.Content
			}
		}
		t.Fatalf("v%s has no design-system.json: %s", version, out)
		return ""
	}
	if out, err := run(manifest("#1d4ed8"), "create", "harbor", "--name", "Harbor tokens", "--file", "-"); err != nil {
		t.Fatalf("create: %v %s", err, out)
	}
	if out, err := run(manifest("#0f766e"), "publish", "harbor", "--base-version", "1", "--file", "-"); err != nil {
		t.Fatalf("publish: %v %s", err, out)
	}
	if out, err := run(manifest("#000000"), "publish", "harbor", "--base-version", "1", "--file", "-"); err == nil {
		t.Fatalf("stale base version accepted: %s", out)
	}
	if out, err := run("", "restore", "harbor", "--base-version", "2", "--version", "1"); err != nil {
		t.Fatalf("restore: %v %s", err, out)
	}
	if got := tokensAt("2"); !strings.Contains(got, "#0f766e") {
		t.Fatalf("v2 tokens: %s", got)
	}
	if v1, v3 := tokensAt("1"), tokensAt("3"); v1 != v3 || !strings.Contains(v3, "#1d4ed8") {
		t.Fatalf("restore did not reproduce v1 tokens: v1=%s v3=%s", v1, v3)
	}
	out, err := run("", "versions", "harbor")
	var head struct {
		LatestVersion int64 `json:"latest_version"`
	}
	if err != nil || json.Unmarshal(out, &head) != nil || head.LatestVersion != 3 {
		t.Fatalf("versions: %v %s", err, out)
	}
}
