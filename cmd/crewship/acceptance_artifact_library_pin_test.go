package main

import (
	"bytes"
	"encoding/base64"
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

// A Library's owner publishes a version; a consumer project pins a
// self-contained copy, reviews the update and applies it explicitly. Library
// edits and restores never change the consumer (#5189). Every step drives the
// CLI binary, which is the contract agents use.
func TestAcceptance_LibraryPinBinary(t *testing.T) {
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
	const workspace = "clibrarypincliworkspacexx"
	const token = "crewship_cli_library_pin_binary_fixture"
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('library-pin-cli-owner','library-pin-cli-owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('clibrarypincliworkspacexx','Library Pins','library-pin-cli')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('library-pin-cli-member','clibrarypincliworkspacexx','library-pin-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('library-pin-cli-token','library-pin-cli-owner','fixture',?,'STANDARD','["pages:*"]')`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := journal.NewWriter(db, logger, journal.WriterOptions{ChainKey: bytes.Repeat([]byte{6}, 32)})
	t.Cleanup(func() { _ = audit.Close() })
	router, err := api.NewRouter(db, "library-pin-cli-synthetic-secret", logger, api.WithJournal(audit))
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
	// The second library version carries an OFL font, its licence and a
	// proprietary font (#5248).
	inter := append([]byte("wOF2"), bytes.Repeat([]byte{7}, 200)...)
	manifest := func(primary string) string {
		tokens := `{"schema_version":1,"name":"Harbor","colors":{"primary":"` + primary + `"},"typography":{"body":{"size":16,"weight":400,"font_family":"Inter, sans-serif"}},"spacing":{"md":16},"radii":{"sm":4}}`
		files := []map[string]string{
			{"path": "index.html", "media_type": "text/html", "content": "<h1 id=title>Harbor tokens</h1>"},
			{"path": "design-system.json", "media_type": "application/json", "content": tokens},
		}
		if primary == "#0f766e" {
			files = append(files,
				map[string]string{"path": "fonts.json", "media_type": "application/json", "content": `{"schema_version":1,"fonts":[{"file":"fonts/Inter.woff2","family":"Inter","weight":400},{"file":"fonts/Secret.woff2","family":"Secret","license":"LicenseRef-Proprietary"}]}`},
				map[string]string{"path": "fonts/Inter.woff2", "media_type": "font/woff2", "content": "data:font/woff2;base64," + base64.StdEncoding.EncodeToString(inter)},
				map[string]string{"path": "fonts/Secret.woff2", "media_type": "font/woff2", "content": "data:font/woff2;base64," + base64.StdEncoding.EncodeToString([]byte("wOF2secret"))},
				map[string]string{"path": "fonts/OFL.txt", "media_type": "text/plain", "content": "This Font Software is licensed under the SIL Open Font License, Version 1.1.\n\nSIL OPEN FONT LICENSE Version 1.1 - 26 February 2007\n"})
		}
		body, err := json.Marshal(map[string]any{"entrypoint": "index.html", "files": files})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	decode := func(out []byte, err error) map[string]any {
		t.Helper()
		if err != nil {
			t.Fatalf("command failed: %v %s", err, out)
		}
		got := map[string]any{}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatalf("decode: %v %s", err, out)
		}
		return got
	}
	decode(run(manifest("#1d4ed8"), "create", "harbor", "--name", "Harbor tokens", "--file", "-"))
	deck, err := json.Marshal(map[string]any{"entrypoint": "index.html", "files": []map[string]string{{"path": "index.html", "media_type": "text/html", "content": "<h1>Deck</h1>"}}})
	if err != nil {
		t.Fatal(err)
	}
	decode(run(string(deck), "create", "deck", "--name", "Deck", "--file", "-"))

	if got := decode(run("", "library", "show", "harbor")); got["published_version"] != float64(0) || got["can_publish"] != true {
		t.Fatalf("library show before publish: %v", got)
	}
	if out, err := run("", "pin", "set", "deck", "--library", "harbor", "--version", "1", "--if-pinned-version", "0"); err == nil {
		t.Fatalf("pinned an unpublished library: %s", out)
	}
	if out, err := run("", "pin", "set", "deck", "--library", "harbor"); err == nil || !strings.Contains(string(out), "if-pinned-version") {
		t.Fatalf("pin without the reviewed version: %v %s", err, out)
	}
	decode(run("", "library", "publish", "harbor", "--version", "1"))
	pin := decode(run("", "pin", "set", "deck", "--library", "harbor", "--version", "1", "--if-pinned-version", "0"))
	if pin["library_version"] != float64(1) || pin["changed"] != true || !strings.Contains(fmt.Sprint(pin["tokens"]), "#1d4ed8") {
		t.Fatalf("pin set: %v", pin)
	}
	if again := decode(run("", "pin", "set", "deck", "--library", "harbor", "--version", "1", "--if-pinned-version", "0")); again["changed"] != false {
		t.Fatalf("pin is not idempotent: %v", again)
	}

	// v2 changes a token; v3 restores v1. Restore never touches the pin.
	decode(run(manifest("#0f766e"), "publish", "harbor", "--base-version", "1", "--file", "-"))
	decode(run("", "restore", "harbor", "--base-version", "2", "--version", "1"))
	if got := decode(run("", "pin", "show", "deck")); got["library_version"] != float64(1) || got["sha256"] != pin["sha256"] {
		t.Fatalf("restore changed the pin: %v", got)
	}
	if got := decode(run("", "pin", "check", "deck")); got["update_available"] != false {
		t.Fatalf("update before republish: %v", got)
	}
	decode(run("", "library", "publish", "harbor", "--version", "2"))
	review := decode(run("", "pin", "check", "deck"))
	changes, _ := review["changes"].(map[string]any)
	if review["update_available"] != true || review["published_version"] != float64(2) || fmt.Sprint(changes["changed"]) != "[colors.primary]" {
		t.Fatalf("pin check: %v", review)
	}
	fileChanges, _ := review["file_changes"].(map[string]any)
	if fmt.Sprint(fileChanges["added"]) != "[fonts/Inter.woff2 fonts/OFL.txt fonts/Secret.woff2]" || fmt.Sprint(review["not_copied"]) != "[fonts/Secret.woff2:licence]" {
		t.Fatalf("pin check files: %v", review)
	}
	if out, err := run("", "pin", "set", "deck", "--library", "harbor", "--version", "2", "--if-pinned-version", "0"); err == nil {
		t.Fatalf("stale update accepted: %s", out)
	}
	if out, err := run("", "pin", "set", "deck", "--library", "harbor", "--version", "3", "--if-pinned-library", "harbor", "--if-pinned-version", "1"); err == nil {
		t.Fatalf("unreviewed publication accepted: %s", out)
	}
	if got := decode(run("", "pin", "set", "deck", "--library", "harbor", "--version", "2", "--if-pinned-library", "harbor", "--if-pinned-version", "1")); got["library_version"] != float64(2) || !strings.Contains(fmt.Sprint(got["tokens"]), "#0f766e") {
		t.Fatalf("apply update: %v", got)
	}
	// The pin now carries the OFL font and its licence, byte for byte, and
	// lists the proprietary font without bytes.
	carried := decode(run("", "pin", "files", "deck"))
	byPath := map[string]map[string]any{}
	for _, raw := range carried["files"].([]any) {
		f := raw.(map[string]any)
		byPath[f["path"].(string)] = f
	}
	if f := byPath["fonts/Inter.woff2"]; f["status"] != "copied" || f["family"] != "Inter" || f["content"] != base64.StdEncoding.EncodeToString(inter) {
		t.Fatalf("pinned font: %v", f)
	}
	if f := byPath["fonts/Secret.woff2"]; f["status"] != "licence" || f["content"] != "" {
		t.Fatalf("proprietary font: %v", f)
	}
	if byPath["fonts/OFL.txt"]["status"] != "copied" {
		t.Fatalf("licence file: %v", carried)
	}
	// A later library restore does not touch the carried files.
	decode(run("", "restore", "harbor", "--base-version", "3", "--version", "1"))
	if again := decode(run("", "pin", "files", "deck")); fmt.Sprint(again) != fmt.Sprint(carried) {
		t.Fatalf("restore changed the pinned files: %v", again)
	}
	if out, err := run("", "library", "publish", "harbor", "--version", "0"); err == nil {
		t.Fatalf("zero version accepted: %s", out)
	}
}
