package api

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// The real `crewship page artifact data` binary, through the production Router
// and auth, writes as an Edit-share holder and is refused as a Comment holder
// (#4732). This is the contract agents and people script against.
func TestDashboardDataEditShareThroughBinary(t *testing.T) {
	h, ws, _, page := dashboardEditFixture(t)
	const secret = "dashboard-data-edit-fixture-secret-32"
	router, err := NewRouter(h.db, secret, newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	host := httptest.NewServer(router)
	t.Cleanup(host.Close)
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	var slug string
	if err := h.db.QueryRow(`SELECT slug FROM workspaces WHERE id=?`, ws).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "crewship")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/crewship")
	build.Dir = "../.."
	build.Env = nestedGoBuildEnv(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual binary: %v %s", err, out)
	}
	run := func(t *testing.T, user, stdin string, args ...string) (string, error) {
		t.Helper()
		session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "fixture", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		bearer, err := validator.IssueAccessToken(user, session.ID, "Fixture", user+"@example.test")
		if err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(t.TempDir(), "cli.yaml")
		if err := os.WriteFile(config, []byte("server: "+host.URL+"\nworkspace: "+slug+"\ntoken: "+bearer+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append(args, "--format", "json")...)
		cmd.Dir = dir
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "CREWSHIP_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "CREWSHIP_CONFIG="+config, "CREWSHIP_NO_SLUG_CACHE=1")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		// The CLI pretty-prints JSON; compare against its compact form.
		return strings.Join(strings.Fields(string(out)), ""), err
	}
	t.Run("editor writes and reads latest", func(t *testing.T) {
		out, err := run(t, "data-editor", `{"base_version":2,"documents":[{"id":"cli-row","ifVersion":0,"value":{"amount":7}}]}`, "page", "artifact", "data", "batch", "three-screens", "sales", "--file", "-")
		if err != nil || !strings.Contains(out, `"amount":7`) || !strings.Contains(out, `"source_label":"You"`) {
			t.Fatalf("editor batch: exit=%v %s", err, out)
		}
		var actor string
		if err := h.db.QueryRow(`SELECT updated_by FROM artifact_data_documents WHERE page_id=? AND id='cli-row'`, page).Scan(&actor); err != nil || actor != "data-editor" {
			t.Fatalf("binary write actor %q %v", actor, err)
		}
		out, err = run(t, "data-editor", "", "page", "artifact", "data", "get", "three-screens", "sales", "--version", "2")
		if err != nil || !strings.Contains(out, "cli-row") || !strings.Contains(out, `"seed"`) {
			t.Fatalf("editor read latest: exit=%v %s", err, out)
		}
		out, err = run(t, "data-editor", `{"base_version":2,"documents":[{"id":"cli-row","ifVersion":0,"value":{"amount":8}}]}`, "page", "artifact", "data", "batch", "three-screens", "sales", "--file", "-")
		if err == nil || !strings.Contains(out, "409") {
			t.Fatalf("editor CAS through binary: exit=%v %s", err, out)
		}
		out, err = run(t, "data-editor", `{"base_version":1,"documents":[{"id":"pin-row","ifVersion":0,"value":{"amount":8}}]}`, "page", "artifact", "data", "batch", "three-screens", "sales", "--file", "-")
		if err == nil || !strings.Contains(out, "409") {
			t.Fatalf("editor shared-pin write through binary: exit=%v %s", err, out)
		}
	})
	t.Run("commenter refused", func(t *testing.T) {
		before := dashboardDataSnapshot(t, h, page)
		out, err := run(t, "data-commenter", `{"base_version":1,"documents":[{"id":"comment-row","ifVersion":0,"value":{"amount":1}}]}`, "page", "artifact", "data", "batch", "three-screens", "sales", "--file", "-")
		if err == nil || !strings.Contains(out, "403") {
			t.Fatalf("commenter batch: exit=%v %s", err, out)
		}
		if after := dashboardDataSnapshot(t, h, page); after != before {
			t.Fatal("commenter binary write changed data")
		}
	})
}
