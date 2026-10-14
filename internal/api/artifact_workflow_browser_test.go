//go:build linux && (artifact_workflow_browser || conformance)

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

// Preauthenticated synthetic DB sessions exercise the real Router and static
// frontend. This is not login, live-agent execution, Docker or dev4 evidence.
func TestArtifactWorkflowBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_ARTIFACT_WORKFLOW_BROWSER") != "1" {
		t.Fatal("tagged execution requires CREWSHIP_ARTIFACT_WORKFLOW_BROWSER=1, a real static export and provisioned Chromium")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build the real static frontend before running this test", err)
	}
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	const commenter = "artifact-browser-commenter"
	execOrFatal(t, db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'Synthetic commenter')`, commenter, commenter+"@example.test")
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('artifact-browser-member',?,?,'VIEWER')`, workspace, commenter)
	const secret = "synthetic-artifact-browser-session-secret-long-enough"
	validator, err := auth.NewJWTValidator(secret)
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{owner, commenter} {
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
	hubContext, stopHub := context.WithCancel(t.Context())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); hub.Run(hubContext) }()
	t.Cleanup(func() { stopHub(); <-hubDone })
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-artifact-browser-internal"), WithStoragePath(t.TempDir()), WithHub(hub))
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
	fixture, err := json.Marshal(map[string]any{"server": server.URL, "workspace": workspace, "owner": owner, "commenter": commenter, "tokens": tokens})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "artifact-browser.json")
	if err = os.WriteFile(path, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-artifact-workflow-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_ARTIFACT_WORKFLOW_FIXTURE="+path)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("preauthenticated artifact workflow browser", err)
	}
	var boardSeq, boardElements int
	if err = db.QueryRow(`SELECT b.seq,(SELECT COUNT(*) FROM page_board_elements e WHERE e.board_page_id=b.page_id AND e.element_json IS NOT NULL) FROM page_boards b JOIN pages p ON p.id=b.page_id WHERE p.slug='browser-board' AND p.workspace_id=?`, workspace).Scan(&boardSeq, &boardElements); err != nil || boardSeq != 2 || boardElements != 2 {
		t.Fatalf("real Board DB seq=%d elements=%d err=%v", boardSeq, boardElements, err)
	}
	var head, threads, requests int
	if err = db.QueryRow(`SELECT head_rev FROM page_artifacts JOIN pages ON pages.id=page_artifacts.page_id WHERE pages.workspace_id=? AND pages.slug='browser-report'`, workspace).Scan(&head); err != nil || head != 3 {
		t.Fatalf("real DB head=%d err=%v", head, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM page_artifact_threads t JOIN pages p ON p.id=t.page_id WHERE p.workspace_id=? AND p.slug='browser-report' AND t.revision=1`, workspace).Scan(&threads); err != nil || threads != 1 {
		t.Fatalf("original anchor count=%d err=%v", threads, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM page_artifact_requests`).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("commenter created work count=%d err=%v", requests, err)
	}
}
