//go:build linux && (docs_structure_browser || conformance)

package api

import (
	"context"
	"encoding/json"
	"fmt"
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

// Production UI admission proof; work remains queued, with no model execution.
// The separate Foundation exercise qualifies compiled agent CLI execution.
func TestDocumentsStructuralBrowser(t *testing.T) {
	if os.Getenv("CREWSHIP_DOCS_STRUCTURAL_BROWSER") != "1" {
		t.Fatal("requires explicit browser opt-in, production export and matched Docs engine")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "out/index.html")); err != nil {
		t.Fatal("build production export first", err)
	}
	dir, err := os.MkdirTemp("", "docs-structure-ui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	secretFile := filepath.Join(dir, "token")
	if err = os.WriteFile(secretFile, []byte(strings.Repeat("owned-structural-fixture-", 3)), 0600); err != nil {
		t.Fatal(err)
	}
	h, workspace, user, _, _ := artifactRunFixture(t)
	const page, slug = "structure-browser-page", "structure-browser"
	wmSeedPage(t, h.db, workspace, page, slug, "Structural editing", user, "")
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'agent','artifact-agent','write',?)`, page, user)
	jwtSecret := strings.Repeat("structural-ui-jwt-", 3)
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithInternalToken(h.artifactMasterToken), WithDocumentAuthority(filepath.Join(dir, "a"), secretFile), WithDocumentService(filepath.Join(dir, "n")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	node := exec.CommandContext(t.Context(), "node", filepath.Join(root, "tools/docs-service/service.mjs"))
	node.Env = append(os.Environ(), "CREWSHIP_DOCS_SOCKET="+filepath.Join(dir, "n"), "CREWSHIP_DOCS_AUTHORITY_SOCKET="+filepath.Join(dir, "a"), "CREWSHIP_DOCS_SERVICE_TOKEN_FILE="+secretFile)
	node.Stderr = os.Stderr
	if err = node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Process.Kill(); _ = node.Wait() })
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "n"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	ready := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		request, _ := http.NewRequestWithContext(t.Context(), "GET", "http://docs/health/ready", nil)
		request.Header.Set("X-Crewship-Docs-Service", strings.Repeat("owned-structural-fixture-", 3))
		if response, e := client.Do(request); e == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
	}
	if !ready {
		t.Fatal("matched engine not ready")
	}
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "owned-structural-browser", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	token, err := validator.IssueAccessToken(user, session.ID, user, user+"@example.invalid")
	if err != nil {
		t.Fatal(err)
	}
	content := []any{}
	for index := 0; index < 200; index++ {
		content = append(content, map[string]any{"type": "paragraph", "attrs": map[string]any{"id": fmt.Sprintf("p%d", index)}, "content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("Saved block %d", index)}}})
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", router)
	mux.Handle("/", StaticFileHandler(os.DirFS(filepath.Join(root, "out"))))
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	raw, _ := json.Marshal(map[string]any{"id": "structural-browser-init", "schema": 3, "body": map[string]any{"type": "doc", "content": content}})
	request, _ := http.NewRequestWithContext(t.Context(), "POST", host.URL+"/api/v1/pages/"+slug+"/document/initialize?workspace_id="+workspace, strings.NewReader(string(raw)))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := host.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("initialize actual structural browser document", response.StatusCode)
	}
	raw, _ = json.Marshal(map[string]any{"server": host.URL, "workspace": workspace, "user_id": user, "token": token, "slug": slug, "chat_id": "artifact-chat"})
	fixture := filepath.Join(t.TempDir(), "fixture.json")
	if err = os.WriteFile(fixture, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", filepath.Join(root, "scripts/acceptance-docs-structure-browser.mjs"))
	command.Env = append(os.Environ(), "CREWSHIP_DOCS_STRUCTURE_FIXTURE="+fixture)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err = command.Run(); err != nil {
		t.Fatal("actual structural browser", err)
	}
	var count int
	var targets string
	if err = h.db.QueryRow(`SELECT count(*),targets_json FROM document_requests WHERE page_id=?`, page).Scan(&count, &targets); err != nil || count != 1 {
		t.Fatal("browser duplicated/lost admission", count, err)
	}
	var saved []documentVerifiedTarget
	if json.Unmarshal([]byte(targets), &saved) != nil || len(saved) != 1 || saved[0].Structure == nil || len(saved[0].Structure.Blocks) != 200 {
		t.Fatal("Select all silently narrowed or promoted scope")
	}
}
