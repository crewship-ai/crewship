//go:build linux && (member_navigation_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// The supported httptest/static-export harness exercises production routes and
// browser sessions. It creates no runtime, Docker containers or external users.
func TestMemberNavigationBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_MEMBER_NAV_BROWSER") != "1" {
		t.Fatal("tagged browser execution requires CREWSHIP_MEMBER_NAV_BROWSER=1 and provisioned static export/Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build the real static frontend before this test", err)
	}
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	execOrFatal(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('nav-empty','Empty workspace','nav-empty')`)
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('nav-empty-owner','nav-empty',?,'OWNER')`, owner)
	store := access.Store{DB: db}
	const secret = "synthetic-navigation-browser-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	members := map[string]string{}
	for _, user := range []string{owner, "nav-client-a", "nav-client-b"} {
		if user != owner {
			execOrFatal(t, db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, user, user+"@example.test", user)
			execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'VIEWER')`, user, ws, user)
			execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'VIEWER')`, user+"-empty", "nav-empty", user)
			member, e := store.Membership(t.Context(), user, ws)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = store.Replace(t.Context(), owner, user, ws, "restricted", member, nil); e != nil {
				t.Fatal(e)
			}
			members[user] = member.ID
		}
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	seedAgentRow(t, db, "nav-alex", ws, seedTestCrew(t, db, ws), "Alex", "alex", "AGENT")
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-nav-internal-token"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.Handle("/ws", router)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	fixture, err := json.Marshal(map[string]any{"server": server.URL, "workspace": ws, "empty_workspace": "nav-empty", "tokens": tokens, "members": members, "owner": owner})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "navigation-browser.json")
	if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-member-navigation-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_MEMBER_NAV_FIXTURE="+fixturePath)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("member navigation production frontend browser", err)
	}
}
