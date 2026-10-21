package main

// Acceptance for `crewship workspace update --appearance-default` (WSD-01):
// the workspace default look, set by an OWNER or ADMIN and inherited by every
// member who has not chosen their own.
//
// A real CLI process against the real router on a migrated SQLite: the value
// has to survive the server's validator
// (internal/api/workspace_appearance_default.go), come back through
// `workspace get`, and be refused for roles below ADMIN.

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

func TestAcceptance_WorkspaceUpdate_AppearanceDefault(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	const ws = "cappdefaultaccws0001"
	roles := map[string]string{"OWNER": "owner", "ADMIN": "admin", "MANAGER": "manager", "MEMBER": "member"}
	tokens := map[string]string{}
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES(?,?,?)`, ws, "Look", "look-cli"); err != nil {
		t.Fatal(err)
	}
	for role, name := range roles {
		token := "crewship_cli_appdefault" + name + strings.Repeat("0", 20)
		tokens[role] = token
		for _, q := range [][]any{
			{`INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, "ad-" + name, name + "@example.invalid", name},
			{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,?)`, "ad-m-" + name, ws, "ad-" + name, role},
			{`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "ad-t-" + name, "ad-" + name, sha256HexToken(token)},
		} {
			if _, err := db.Exec(q[0].(string), q[1:]...); err != nil {
				t.Fatal(err)
			}
		}
	}
	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	binary := buildCrewshipBinary(t)

	configFor := func(role string) string {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\nworkspace: "+ws+"\ntoken: "+tokens[role]+"\nformat: json\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	run := func(role string, args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+configFor(role), "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(role string, args ...string) string {
		out, err := run(role, args...)
		if err != nil {
			t.Fatalf("CLI %s %v: %v\n%s", role, args, err, out)
		}
		return out
	}
	look := func(role string) map[string]any {
		var got struct {
			AppearanceDefault map[string]any `json:"appearance_default"`
		}
		out := must(role, "workspace", "get")
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("workspace get: %v\n%s", err, out)
		}
		return got.AppearanceDefault
	}

	if got := look("MEMBER"); len(got) != 0 {
		t.Fatalf("fresh workspace carries a default look: %v", got)
	}

	// ADMIN sets it inline; the mutation prints a receipt; every role reads it.
	out := must("ADMIN", "workspace", "update", "--appearance-default", `{"style":"slate","knobs":{"radius":"sharp"},"accent":"teal","theme":"dark"}`)
	if !strings.Contains(out, "Workspace updated") {
		t.Fatalf("no receipt: %s", out)
	}
	for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER"} {
		got := look(role)
		knobs, _ := got["knobs"].(map[string]any)
		if got["style"] != "slate" || got["accent"] != "teal" || got["theme"] != "dark" || knobs["radius"] != "sharp" {
			t.Fatalf("%s sees %v", role, got)
		}
	}

	// MEMBER and MANAGER are refused and nothing changes.
	for _, role := range []string{"MEMBER", "MANAGER"} {
		if out, err := run(role, "workspace", "update", "--appearance-default", `{"theme":"light"}`); err == nil || !strings.Contains(out, "403") && !strings.Contains(strings.ToLower(out), "forbidden") && !strings.Contains(strings.ToLower(out), "permission") {
			t.Fatalf("%s was not refused: %v\n%s", role, err, out)
		}
	}
	if got := look("OWNER"); got["theme"] != "dark" {
		t.Fatalf("a refused update changed the default: %v", got)
	}

	// @file replaces the whole object; the server's vocabulary is enforced.
	file := filepath.Join(t.TempDir(), "look.json")
	if err := os.WriteFile(file, []byte("{\n  \"theme\": \"light\"\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	must("OWNER", "workspace", "update", "--appearance-default", "@"+file)
	if got := look("OWNER"); got["theme"] != "light" || len(got) != 1 {
		t.Fatalf("@file did not replace the default: %v", got)
	}
	if out, err := run("OWNER", "workspace", "update", "--appearance-default", `{"theme":"sepia"}`); err == nil || !strings.Contains(out, "dark, light or system") {
		t.Fatalf("an unknown theme was accepted: %v\n%s", err, out)
	}
	if out, err := run("OWNER", "workspace", "update", "--appearance-default", `{"density":"compact"}`); err == nil || !strings.Contains(out, "style, knobs, accent and theme") {
		t.Fatalf("an unknown key was accepted: %v\n%s", err, out)
	}
	if out, err := run("OWNER", "workspace", "update", "--appearance-default", `"dark"`); err == nil || !strings.Contains(out, "JSON object") {
		t.Fatalf("a bare string was accepted: %v\n%s", err, out)
	}

	// An unrelated update leaves it alone; the human view names it.
	must("ADMIN", "workspace", "update", "--name", "Look renamed")
	human := must("MEMBER", "workspace", "get", "--format", "table")
	if !strings.Contains(human, "Appearance default") || !strings.Contains(human, "theme=light") {
		t.Fatalf("human view omits the default:\n%s", human)
	}

	// {} resets.
	must("ADMIN", "workspace", "update", "--appearance-default", "{}")
	if got := look("MEMBER"); len(got) != 0 {
		t.Fatalf("{} did not reset the default: %v", got)
	}
}
