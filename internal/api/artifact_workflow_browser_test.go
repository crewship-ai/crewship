//go:build linux && (artifact_workflow_browser || conformance)

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
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
	// Test-only fixture insertion on the owned loopback server. Production
	// rate limits remain enabled; browsing still uses the real authorized API.
	mux.HandleFunc("POST /_test/retained-discussions", func(w http.ResponseWriter, r *http.Request) {
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "fixture transaction", 500)
			return
		}
		defer tx.Rollback()
		var pageID string
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM pages WHERE workspace_id=? AND slug='browser-report'", workspace).Scan(&pageID); err != nil {
			http.Error(w, "fixture Page", 500)
			return
		}
		for index := 0; index < 101; index++ {
			id := fmt.Sprintf("pagination-thread-%03d", index)
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_artifact_threads(id,page_id,revision,anchor_json,status,created_by_user_id,created_at,updated_at) VALUES(?,?,1,'{"schema_version":1,"type":"document"}','resolved',?,'2026-10-15T12:00:00Z','2026-10-15T12:00:00Z')`, id, pageID, commenter); err != nil {
				http.Error(w, "fixture thread", 500)
				return
			}
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO page_artifact_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES(?,?,1,?,?,'2026-10-15T12:00:00Z')`, fmt.Sprintf("pagination-comment-%03d", index), id, fmt.Sprintf("Retained pagination discussion %d", index), commenter); err != nil {
				http.Error(w, "fixture comment", 500)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "fixture commit", 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	mux.Handle("/api/", router)
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	var pngInput bytes.Buffer
	if err = png.Encode(&pngInput, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	fixture, err := json.Marshal(map[string]any{"server": server.URL, "workspace": workspace, "owner": owner, "commenter": commenter, "tokens": tokens, "image_png": pngInput.Bytes()})
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
	if err = db.QueryRow(`SELECT b.seq,(SELECT COUNT(*) FROM page_board_elements e WHERE e.board_page_id=b.page_id AND e.element_json IS NOT NULL) FROM page_boards b JOIN pages p ON p.id=b.page_id WHERE p.slug='browser-board' AND p.workspace_id=?`, workspace).Scan(&boardSeq, &boardElements); err != nil || boardSeq != 8 || boardElements != 2 {
		t.Fatalf("real Board DB seq=%d elements=%d err=%v", boardSeq, boardElements, err)
	}
	var head, threads, requests int
	if err = db.QueryRow(`SELECT head_rev FROM page_artifacts JOIN pages ON pages.id=page_artifacts.page_id WHERE pages.workspace_id=? AND pages.slug='browser-report'`, workspace).Scan(&head); err != nil || head != 3 {
		t.Fatalf("real DB head=%d err=%v", head, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM page_artifact_threads t JOIN pages p ON p.id=t.page_id WHERE p.workspace_id=? AND p.slug='browser-report' AND t.revision=1`, workspace).Scan(&threads); err != nil || threads != 102 {
		t.Fatalf("retained original-revision anchor count=%d err=%v", threads, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM page_artifact_requests`).Scan(&requests); err != nil || requests != 0 {
		t.Fatalf("commenter created work count=%d err=%v", requests, err)
	}
}
