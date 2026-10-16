//go:build linux && (docs_rich_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
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

// Two owned human identities use the actual Router, SQLite, managed Node engine
// and production static UI. No response mocks, shared login or model execution.
func TestDocumentsRichBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCS_RICH_BROWSER") != "1" {
		t.Fatal("requires explicit browser opt-in, production export, pinned Docs Node dependencies and Chromium")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "out/index.html")); err != nil {
		t.Fatal("build the production export first", err)
	}
	directory, err := os.MkdirTemp("", "docs-rich-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	authoritySocket, nodeSocket, secretFile := filepath.Join(directory, "a"), filepath.Join(directory, "n"), filepath.Join(directory, "token")
	secret := strings.Repeat("synthetic-rich-service-", 3)
	if err = os.WriteFile(secretFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	f := newInstanceFixture(t)
	f.r.Shutdown()
	f.r, err = NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithDocumentAuthority(authoritySocket, secretFile), WithDocumentService(nodeSocket))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.r.Shutdown)
	node := exec.Command("node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+nodeSocket, "CREWSHIP_DOCS_AUTHORITY_SOCKET="+authoritySocket, "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	node.Stderr = os.Stderr
	if err = node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", nodeSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ready := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", "http://docs/health/ready", nil)
		req.Header.Set("X-Crewship-Docs-Service", secret)
		if response, e := client.Do(req); e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("actual matched Docs engine not ready")
	}
	seedInstanceUser(t, f.db, "rich-writer", "rich-writer@example.invalid", "ws-old", "MEMBER")
	validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{}
	for _, user := range []string{"boss", "rich-writer"} {
		session, e := sessions.NewDBStore(f.db).Create(t.Context(), user, "synthetic-rich-browser", "127.0.0.1", auth.RefreshTokenTTL)
		if e != nil {
			t.Fatal(e)
		}
		tokens[user], e = validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
		if e != nil {
			t.Fatal(e)
		}
	}
	wmSeedPage(t, f.db, "ws-old", "rich-browser-page", "rich-browser", "Rich document", "boss", "")
	execOrFatal(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('rich-browser-page','user','rich-writer','write','boss')`)
	input := `{"id":"browser-init","schema":2,"body":{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"p"},"content":[{"type":"text","text":"prefix middle suffix"}]},{"type":"paragraph","attrs":{"id":"code"},"content":[{"type":"text","text":"literal"}]}]}}`
	wantCode(t, f.slideDo(tokens["boss"], "POST", "/api/v1/pages/rich-browser/document/initialize", input), 200, "initialize actual schema2")
	mux := http.NewServeMux()
	mux.Handle("/api/", f.r)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(root, "out"))))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	raw, err := json.Marshal(map[string]any{"server": server.URL, "workspace": "ws-old", "owner_token": tokens["boss"], "writer_token": tokens["rich-writer"]})
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "rich-browser.json")
	if err = os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(root, "scripts/acceptance-docs-rich-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_DOCS_RICH_FIXTURE="+fixture)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("actual rich Docs browser", err)
	}
	var schema, receipts int
	if err = f.db.QueryRow(`SELECT schema_version FROM documents WHERE page_id='rich-browser-page'`).Scan(&schema); err != nil || schema != 3 {
		t.Fatalf("final rich schema=%d %v", schema, err)
	}
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE page_id='rich-browser-page' AND kind='upgrade'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("duplicate upgrade receipt=%d %v", receipts, err)
	}
}
