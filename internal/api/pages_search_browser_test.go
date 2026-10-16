//go:build linux && (page_search_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestNativeSearchBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_PAGE_SEARCH_BROWSER") != "1" {
		t.Fatal("requires explicit browser gate with real static export")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(repo, "out", "index.html")); err != nil {
		t.Fatal("build actual frontend first", err)
	}
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	const reader = "search-browser-reader"
	execOrFatal(t, db, `INSERT INTO users(id,email,full_name) VALUES(?,?,'Synthetic search reader')`, reader, reader+"@example.test")
	execOrFatal(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('search-browser-membership',?,?,'MEMBER')`, workspace, reader)
	const secret = "synthetic-search-browser-jwt-secret-long-enough"
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
	dir := t.TempDir()
	authoritySocket, nodeSocket := filepath.Join(dir, "a.sock"), filepath.Join(dir, "n.sock")
	secretFile := filepath.Join(dir, "service-secret")
	const serviceSecret = "synthetic-search-browser-service-secret"
	if err = os.WriteFile(secretFile, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(db, secret, newTestLogger(), WithInternalToken("synthetic-search-browser-internal"), WithStoragePath(t.TempDir()), WithHub(hub), WithDocumentAuthority(authoritySocket, secretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	child := exec.Command("node", filepath.Join(repo, "tools/docs-service/service.mjs"))
	child.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	nativeSearchBrowserWaitReady(t, nodeSocket, serviceSecret)

	for _, id := range []string{"search-browser-original", "search-browser-hidden"} {
		execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, id, workspace, id, id, owner)
		deck := slideTestDeck(t, id)
		deck.Slides[0].Elements = []slides.Element{{ID: "search-leaf", Type: "text", X: 0, Y: 0, Width: 400, Height: 100, Text: &slides.Text{Value: "needle second canonical native text", FontFamily: "Arial", FontSize: 24, Color: "#000000", Align: "left"}}}
		for i := range deck.Slides {
			deck.Slides[i].Notes = ""
		}
		request := httptest.NewRequest("POST", "/api/v1/pages/"+id+"/slides/initialize?workspace_id="+workspace, strings.NewReader(slideJSON(t, slides.InitializeRequest{PageID: id, SchemaVersion: 1, MutationID: "browser-init", Deck: deck})))
		request.Header.Set("Authorization", "Bearer "+tokens[owner])
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		wantCode(t, response, 200, "actual native initialization")
	}
	execOrFatal(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('search-browser-original','user',?,'read',?)`, reader, owner)
	execOrFatal(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('search-browser-doc',?,'search-browser-doc','Canonical Doc',?,'{}')`, workspace, owner)
	docBody := `{"id":"browser-doc-init","schema":2,"body":{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"browser-doc-leaf"},"content":[{"type":"text","text":"needle second canonical document"}]}]}}`
	docRequest := httptest.NewRequest("POST", "/api/v1/pages/search-browser-doc/document/initialize?workspace_id="+workspace, strings.NewReader(docBody))
	docRequest.Header.Set("Authorization", "Bearer "+tokens[owner])
	docRequest.Header.Set("Content-Type", "application/json")
	docResponse := httptest.NewRecorder()
	router.ServeHTTP(docResponse, docRequest)
	wantCode(t, docResponse, 200, "actual managed Doc initialization")
	execOrFatal(t, db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('search-browser-doc','user',?,'read',?)`, reader, owner)
	var raced atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/pages/resolve/id/search-browser-original" && raced.CompareAndSwap(false, true) {
			tx, err := db.BeginTx(r.Context(), nil)
			if err == nil {
				_, err = tx.ExecContext(r.Context(), `UPDATE pages SET slug='search-browser-renamed' WHERE id='search-browser-original'`)
			}
			if err == nil {
				_, err = tx.ExecContext(r.Context(), `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('search-browser-intruder',?,'search-browser-original','Forbidden reused search body',?,'{}')`, workspace, owner)
			}
			if err == nil {
				err = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if err != nil {
				t.Errorf("rename/reuse fixture: %v", err)
				http.Error(w, "fixture failure", 500)
				return
			}
		}
		router.ServeHTTP(w, r)
	}))
	mux.HandleFunc("POST /fixture-stop-docs", func(w http.ResponseWriter, r *http.Request) {
		if err := child.Process.Kill(); err != nil {
			t.Errorf("stop own Node: %v", err)
			http.Error(w, "fixture stop failure", 500)
			return
		}
		if _, err := db.ExecContext(r.Context(), `DELETE FROM page_search_projections WHERE page_id='search-browser-doc'`); err != nil {
			t.Errorf("clear own cache: %v", err)
			http.Error(w, "fixture cache failure", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/ws", hub.HandleUpgrade)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(repo, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	data, err := json.Marshal(map[string]any{"server": server.URL, "workspace": workspace, "owner": owner, "reader": reader, "tokens": tokens})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "search-browser.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(repo, "scripts/acceptance-page-search-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_PAGE_SEARCH_FIXTURE="+path)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("actual native search browser", err)
	}
	if !raced.Load() {
		t.Fatal("actual search-result rename/slug reuse was not exercised")
	}
}

func nativeSearchBrowserWaitReady(t *testing.T, socket, secret string) {
	t.Helper()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(t.Context(), "GET", "http://docs/health/ready", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Crewship-Docs-Service", secret)
		if response, err := client.Do(request); err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("owned managed search browser service did not become ready")
}
