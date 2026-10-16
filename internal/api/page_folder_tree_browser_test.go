//go:build linux && (folder_tree_browser || conformance)

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

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/ws"
)

// Synthetic preauthenticated accounts, real Router/DB/static export, no HTTP mocks.
func TestFolderTreeBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_FOLDER_TREE_BROWSER") != "1" {
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
	const reader = "folder-browser-reader"
	execOrFatal(t, db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'Synthetic folder reader')`, reader, reader+"@example.test")
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('folder-browser-membership',?,?,'MEMBER')`, workspace, reader)
	const secret = "synthetic-folder-browser-secret-long-enough"
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
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-folder-browser-internal"), WithStoragePath(t.TempDir()), WithHub(hub))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	var crewSlug string
	if err = db.QueryRow(`SELECT slug FROM crews WHERE id=?`, crew).Scan(&crewSlug); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"server": server.URL, "workspace": workspace, "owner": owner, "reader": reader, "crew": crewSlug, "tokens": tokens})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "folder-browser.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-folder-tree-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_FOLDER_TREE_FIXTURE="+path)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("real folder tree browser", err)
	}
	var parent string
	if err = db.QueryRow(`SELECT parent.slug FROM page_folders child JOIN page_folders parent ON parent.id=child.parent_id WHERE child.workspace_id=? AND child.name='Browser child'`, workspace).Scan(&parent); err != nil || parent != "browser-other" {
		t.Fatalf("durable reparent=%s err=%v", parent, err)
	}
}
