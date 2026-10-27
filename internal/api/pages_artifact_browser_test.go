//go:build linux && (artifact_project_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Uses production Router/auth/SQLite and the real static export. The only
// fixture controls grant a synthetic reader and restart this owned Router.
func TestArtifactProjectBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_ARTIFACT_PROJECT_BROWSER") != "1" {
		t.Fatal("explicit browser execution requires CREWSHIP_ARTIFACT_PROJECT_BROWSER=1")
	}
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "pages", "_.html")); err != nil {
		t.Fatal("build production static frontend first", err)
	}
	db := artifactProjectFullDurability(t, setupTestDB(t))
	owner := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, owner)
	reader := "artifact-browser-reader"
	execOrFatal(t, db, `INSERT INTO users(id,email,full_name) VALUES(?,?,?)`, reader, "project-browser-reader@example.test", "Artifact browser reader")
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, reader)
	const secret = "synthetic-artifact-project-browser-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{owner, reader} {
		session, e := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.test")
		if e != nil {
			t.Fatal(e)
		}
	}
	makeRouter := func() *Router {
		router, e := NewRouter(db, secret, newTestLogger())
		if e != nil {
			t.Fatal(e)
		}
		return router
	}
	current := makeRouter()
	var mu sync.RWMutex
	t.Cleanup(func() { mu.Lock(); defer mu.Unlock(); current.Shutdown() })
	mux := http.NewServeMux()
	mux.HandleFunc("POST /__fixture/reader", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Control") != "synthetic-project-control" {
			w.WriteHeader(403)
			return
		}
		var page string
		if db.QueryRowContext(r.Context(), `SELECT id FROM pages WHERE workspace_id=? AND slug='browser-artifact'`, ws).Scan(&page) != nil {
			w.WriteHeader(404)
			return
		}
		_, e := db.ExecContext(r.Context(), `INSERT OR REPLACE INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, reader, owner)
		if e != nil {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /__fixture/restart", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Fixture-Control") != "synthetic-project-control" {
			w.WriteHeader(403)
			return
		}
		next := makeRouter()
		mu.Lock()
		previous := current
		current = next
		mu.Unlock()
		previous.Shutdown()
		w.WriteHeader(204)
	})
	serve := func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		router := current
		mu.RUnlock()
		router.ServeHTTP(w, r)
	}
	mux.HandleFunc("/api/", serve)
	mux.HandleFunc("/ws", serve)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	fixture, err := json.Marshal(map[string]any{"server": server.URL, "workspace": ws, "owner": owner, "reader": reader, "tokens": tokens})
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(t.TempDir(), "artifact-project-browser.json")
	if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "e2e", "artifact-project.browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_ARTIFACT_PROJECT_FIXTURE="+fixturePath)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("artifact project production browser acceptance", err)
	}
}
