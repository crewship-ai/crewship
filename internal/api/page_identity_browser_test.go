//go:build linux && (page_identity_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Synthetic preauthenticated accounts, real Router/DB/static export, no HTTP mocks.
func TestPageIdentityBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_PAGE_IDENTITY_BROWSER") != "1" {
		t.Fatal("requires explicit browser execution and real static export")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build real static frontend first", err)
	}
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	crew := seedTestCrew(t, db, workspace)
	const reader = "identity-browser-reader"
	execOrFatal(t, db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'Synthetic folder reader')`, reader, reader+"@example.test")
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('identity-browser-membership',?,?,'MEMBER')`, workspace, reader)
	const secret = "synthetic-identity-browser-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{owner, reader} {
		session, err := sessions.NewDBStore(db).Create(t.Context(), user, "synthetic", "127.0.0.1", auth.RefreshTokenTTL)
		if err != nil {
			t.Fatal(err)
		}
		tokens[user], err = validator.IssueAccessToken(user, session.ID, user, user+"@example.test")
		if err != nil {
			t.Fatal(err)
		}
	}
	hub := ws.NewHub(newTestLogger(), nil, validator, sessions.NewDBStore(db))
	hub.SetChannelAuthorizer(ws.NewDBChannelAuthorizer(db))
	hubCtx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); hub.Run(hubCtx) }()
	t.Cleanup(func() { stop(); <-done })
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-identity-browser-internal"), WithStoragePath(t.TempDir()), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)

	execOrFatal(t, db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('identity-browser-parent',?,'identity-hidden-parent','Hidden ancestor canary',?,'2026-10-16','2026-10-16')`, workspace, crew)
	execOrFatal(t, db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,parent_id,created_at,updated_at) VALUES('identity-browser-leaf',?,'identity-leaf','Shared leaf',?,'identity-browser-parent','2026-10-16','2026-10-16')`, workspace, crew)
	execOrFatal(t, db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('identity-browser-leaf','user',?,1,0,'2026-10-16')`, reader)
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,owner_crew_id,folder_id,spec_json) VALUES('identity-browser-original',?,'identity-old','Original stable Page',?,?,'identity-browser-leaf','{}')`, workspace, owner, crew)
	var raced atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/pages/identity-old" && raced.CompareAndSwap(false, true) {
			tx, err := db.BeginTx(r.Context(), nil)
			if err == nil {
				_, err = tx.ExecContext(r.Context(), `UPDATE pages SET slug='identity-renamed' WHERE id='identity-browser-original'`)
			}
			if err == nil {
				_, err = tx.ExecContext(r.Context(), `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('identity-browser-intruder',?,'identity-old','Forbidden reused body',?,'{}')`, workspace, owner)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if err != nil {
				t.Errorf("race fixture transaction: %v", err)
				http.Error(w, "fixture transaction failed", 500)
				return
			}
		}
		router.ServeHTTP(w, r)
	}))
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	var crewSlug string
	if err = db.QueryRow(`SELECT slug FROM crews WHERE id=?`, crew).Scan(&crewSlug); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"server": server.URL, "workspace": workspace, "owner": owner, "reader": reader, "crew": crewSlug, "pageID": "identity-browser-original", "tokens": tokens})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "identity-browser.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-page-identity-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_PAGE_IDENTITY_FIXTURE="+path)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("real Page identity browser", err)
	}
	if !raced.Load() {
		t.Fatal("real detail race was not exercised")
	}
}
