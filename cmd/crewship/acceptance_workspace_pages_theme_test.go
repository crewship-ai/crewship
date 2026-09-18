package main

// Acceptance for `crewship workspace update --pages-theme` (#2581): the one
// workspace field that until now could only be set from the browser's
// Settings → General → Pages appearance card.
//
// A real CLI process against the real router on a migrated SQLite, not a
// stub: the value has to survive the server's validator
// (internal/api/workspace_pages_theme.go) and come back through
// `workspace get`, which is what "round-trips" means.

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

func TestAcceptance_WorkspaceUpdate_PagesThemeRoundTrips(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cthemeacceptancews00"
	token := "crewship_cli_pagestheme00000000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('cthemeacceptancews00','Theme','theme-cli')`,
		`INSERT INTO users(id,email,full_name) VALUES('theme-cli-owner','theme@example.invalid','Theme Owner')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('theme-cli-member','cthemeacceptancews00','theme-cli-owner','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES('theme-cli-token','theme-cli-owner','test',?,datetime('now'))`, sha256HexToken(token)); err != nil {
		t.Fatal(err)
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
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
	theme := func() map[string]string {
		var got struct {
			PagesTheme map[string]string `json:"pages_theme"`
		}
		out := must("workspace", "get")
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("workspace get: %v\n%s", err, out)
		}
		return got.PagesTheme
	}

	if got := theme(); len(got) != 0 {
		t.Fatalf("fresh workspace carries a theme: %v", got)
	}

	// Inline JSON.
	must("workspace", "update", "--pages-theme", `{"accent":"#0f766e","background":"#FFFFFF"}`)
	if got := theme(); got["accent"] != "#0f766e" || got["background"] != "#FFFFFF" || len(got) != 2 {
		t.Fatalf("inline theme did not round-trip: %v", got)
	}

	// @file, replacing the whole object rather than merging into it.
	file := filepath.Join(t.TempDir(), "theme.json")
	if err := os.WriteFile(file, []byte("{\n  \"text\": \"#111111\"\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	must("workspace", "update", "--pages-theme", "@"+file)
	if got := theme(); got["text"] != "#111111" || len(got) != 1 {
		t.Fatalf("@file theme did not replace the object: %v", got)
	}

	// The server's vocabulary is enforced, and the CLI relays the refusal
	// rather than filtering keys itself.
	if out, err := run("workspace", "update", "--pages-theme", `{"accent":"teal"}`); err == nil || !strings.Contains(out, "#RRGGBB") {
		t.Fatalf("a named colour was accepted: %v\n%s", err, out)
	}
	if out, err := run("workspace", "update", "--pages-theme", `{"font":"#000000"}`); err == nil || !strings.Contains(out, "accent, background") {
		t.Fatalf("an unknown key was accepted: %v\n%s", err, out)
	}
	if got := theme(); got["text"] != "#111111" || len(got) != 1 {
		t.Fatalf("a refused update changed the theme: %v", got)
	}

	// Not an object: refused locally, before anything is sent.
	if out, err := run("workspace", "update", "--pages-theme", `"#000000"`); err == nil || !strings.Contains(out, "JSON object") {
		t.Fatalf("a bare string was accepted: %v\n%s", err, out)
	}
	if out, err := run("workspace", "update", "--pages-theme", "@"+filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(out, "read --pages-theme file") {
		t.Fatalf("a missing file was accepted: %v\n%s", err, out)
	}

	// {} resets to the SDK defaults, and an update that does not name the
	// flag leaves the theme alone.
	must("workspace", "update", "--pages-theme", "{}")
	if got := theme(); len(got) != 0 {
		t.Fatalf("{} did not reset the theme: %v", got)
	}
	must("workspace", "update", "--pages-theme", `{"border":"#abcdef"}`)
	must("workspace", "update", "--name", "Theme renamed")
	if got := theme(); got["border"] != "#abcdef" {
		t.Fatalf("--name update touched the theme: %v", got)
	}

	// The human view names the colours; a table reader should not have to
	// switch formats to see whether a theme is set.
	human := must("workspace", "get", "--format", "table")
	if !strings.Contains(human, "Pages theme") || !strings.Contains(human, "border=#abcdef") {
		t.Fatalf("human view omits the theme:\n%s", human)
	}
}
