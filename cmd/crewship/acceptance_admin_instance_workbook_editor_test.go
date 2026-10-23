//go:build !clionly

package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/editororigin"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// A real CLI process against a real router: the workbook editor detail is
// for instance administrators only. The same-host editor here has learned
// one public origin, whose check disabled it; the CLI shows the origin, the
// cause and the problem, which the public /healthz never carries.
func TestAcceptance_AdminInstanceWorkbookEditor(t *testing.T) {
	t.Setenv(backup.InstanceOwnerEmailEnv, "")
	db := testutil.MigratedDB(t).DB
	admin, member := "crewship_cli_wbeditoradmin0000000000000", "crewship_cli_wbeditormember000000000000"
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug,created_at) VALUES('we-a','Alpha','alpha','2026-01-01 00:00:00'),('we-b','Beta','beta','2026-06-01 00:00:00')`,
		`INSERT INTO users(id,email,full_name) VALUES('we-boss','boss@alpha.invalid','Boss'),('we-eve','eve@beta.invalid','Eve')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('we-m1','we-a','we-boss','OWNER'),('we-m2','we-b','we-eve','OWNER')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	for id, tok := range map[string]string{"we-boss": admin, "we-eve": member} {
		if _, err := db.Exec(`INSERT INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,'test',?,datetime('now'))`, "tok-"+id, id, sha256HexToken(tok)); err != nil {
			t.Fatal(err)
		}
	}
	editor, err := editororigin.New(editororigin.Options{SameHost: true})
	if err != nil {
		t.Fatal(err)
	}
	learn := httptest.NewRequest("GET", "/api/v1/me/preferences", nil)
	learn.Host = "crewship.example.test"
	learn.Header.Set("Sec-Fetch-Site", "same-origin")
	learn.Header.Set("Referer", "https://crewship.example.test/chat")
	editor.LearnOrigin(learn, "we-boss", func() bool { return true })
	editor.RecordCheck("https://crewship.example.test", editororigin.ProxyCheck{Status: editororigin.ProxyCheckFailed, Isolation: true, Cause: editororigin.CauseRedirect,
		Problems: []string{`/_editor/b/x is redirected (HTTP 302 to "https://sso.example.test/login")`}})

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default(),
		api.WithWorkbookEditor(editor.LearnOrigin, func() any { return editor.Detail() }))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(router)
	defer srv.Close()
	binary := buildCrewshipBinary(t)
	run := func(token string, args ...string) (string, error) {
		cfg := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(cfg, []byte("server: "+srv.URL+"\ntoken: "+token+"\nformat: table\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=", "NO_COLOR=1", "DATABASE_URL=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run(admin, "admin", "instance", "workbook-editor")
	if err != nil {
		t.Fatalf("table: %v\n%s", err, out)
	}
	for _, want := range []string{"same host", "disabled", "https://crewship.example.test", "learned from we-boss (admin)", "redirect", "sso.example.test"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output lacks %q:\n%s", want, out)
		}
	}
	raw, err := run(admin, "admin", "instance", "workbook-editor", "--format", "json")
	if err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	var got workbookEditorState
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, raw)
	}
	if got.Mode != "same-host" || got.Status != editororigin.HealthDisabled || len(got.Origins) != 1 || !got.Origins[0].Disabled || got.Origins[0].Cause != editororigin.CauseRedirect ||
		got.Origins[0].LearnedFrom != "we-boss" || !got.Origins[0].AdminSession {
		t.Fatalf("json = %s", raw)
	}

	// A workspace owner who is not an instance administrator sees nothing.
	if out, err := run(member, "admin", "instance", "workbook-editor"); err == nil || strings.Contains(out, "crewship.example.test") {
		t.Fatalf("non-admin: %v\n%s", err, out)
	}
	resp, err := http.Get(srv.URL + "/api/v1/admin/instance/workbook-editor")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}

	// Without the editor the endpoint answers "off".
	off, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = off
	if out, err := run(admin, "admin", "instance", "workbook-editor"); err != nil || !strings.Contains(out, "off") {
		t.Fatalf("editor off: %v\n%s", err, out)
	}
}
