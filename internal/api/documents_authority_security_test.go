//go:build linux

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentEngineProtocolRecoveryCannotFenceAnIncompatiblePair(t *testing.T) {
	h, _, _, workspace, user, page := artifactFixture(t)
	store := documents.New(h.db, documents.Options{})
	key := documents.Key{WorkspaceID: workspace, PageID: page}
	allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + user, nil }
	if _, err := store.Init(t.Context(), key, 2, []byte("protocol-only fixture"), []byte(`[]`), allow); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(t.Context(), key, "live-engine", allow)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "protocol-recovery-synthetic-service-secret"
	handler := &documentAuthority{pages: h, store: store, secret: secret}
	call := func(body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/internal/docs/v1/engine/recover", strings.NewReader(body))
		r.Header.Set("X-Crewship-Docs-Service", secret)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("recovery status=%d body=%s", w.Code, w.Body)
		}
		return w
	}
	for _, body := range []string{`{"boot":"synthetic-legacy-engine-boot"}`, `{"boot":"synthetic-legacy-engine-boot","protocol":1}`, `{"boot":"synthetic-legacy-engine-boot","protocol":2}`} {
		call(body, 409)
		if _, err := store.Renew(t.Context(), key, lease, allow); err != nil {
			t.Fatalf("incompatible recovery fenced live writer: %v", err)
		}
	}
	w := call(`{"boot":"synthetic-compatible-engine-boot","protocol":3}`, 200)
	var ack struct {
		Protocol  int  `json:"protocol"`
		Recovered bool `json:"recovered"`
	}
	if json.Unmarshal(w.Body.Bytes(), &ack) != nil || ack.Protocol != documentEngineProtocol || !ack.Recovered {
		t.Fatalf("missing compatible recovery acknowledgement: %s", w.Body)
	}
	if _, err := store.Renew(t.Context(), key, lease, allow); err == nil {
		t.Fatal("compatible recovery failed to fence prior writer")
	}
}

func TestDocumentAuthoritySecurityStartupAndIsolation(t *testing.T) {
	h, _, _, workspace, user, page := artifactFixture(t)
	const jwtSecret = "document-authority-security-synthetic-jwt-secret"
	const serviceSecret = "document-authority-security-synthetic-service-secret"
	dir, err := os.MkdirTemp("", "docs-authority-security-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket, secretFile := filepath.Join(dir, "authority.sock"), filepath.Join(dir, "service.token")
	if err := os.WriteFile(secretFile, []byte(serviceSecret), 0600); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithStoragePath(h.artifactStoragePath), WithDocumentAuthority(socket, secretFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	store := documents.New(h.db, documents.Options{})
	key := documents.Key{WorkspaceID: workspace, PageID: page}
	allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + user, nil }
	if _, err := store.Init(t.Context(), key, 1, []byte("synthetic opaque engine snapshot"), []byte(`[]`), allow); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(t.Context(), key, "existing-engine", allow)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("duplicate listener cannot fence live writer", func(t *testing.T) {
		duplicate, err := NewRouter(h.db, jwtSecret, newTestLogger(), WithStoragePath(h.artifactStoragePath), WithDocumentAuthority(socket, secretFile))
		if duplicate != nil {
			duplicate.Shutdown()
		}
		if err == nil {
			t.Fatal("duplicate Router accepted existing socket")
		}
		var writer, owner string
		var epoch, until int64
		if err := h.db.QueryRow(`SELECT writer,owner,epoch,lease_until FROM documents WHERE workspace_id=? AND page_id=?`, workspace, page).Scan(&writer, &owner, &epoch, &until); err != nil {
			t.Fatal(err)
		}
		if writer != lease.Writer || owner != lease.Owner || epoch != lease.Epoch || until != lease.ExpiresAt.UnixMilli() {
			t.Fatal("failed duplicate startup modified active lease")
		}
		if _, err := store.Renew(t.Context(), key, lease, allow); err != nil {
			t.Fatalf("original authority lease no longer renews: %v", err)
		}
	})
	validator, err := auth.NewJWTValidator(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.NewDBStore(h.db).Create(t.Context(), user, "Docs security", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := validator.IssueAccessToken(user, session.ID, user, user+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	request := func(t *testing.T, service, ws string, want int) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), "POST", "http://private/internal/docs/v1/"+page+"/state", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Crewship-Docs-Service", service)
		req.Header.Set("X-Workspace-ID", ws)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		io.Copy(io.Discard, response.Body)
		if response.StatusCode != want {
			t.Fatalf("private authority status=%d want %d", response.StatusCode, want)
		}
	}
	t.Run("service capability required despite human session", func(t *testing.T) {
		request(t, "", workspace, 403)
		request(t, "wrong-service-secret", workspace, 403)
		request(t, serviceSecret, workspace, 200)
	})
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('document-security-foreign','Foreign','document-security-foreign')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('document-security-foreign-member','document-security-foreign',?,'OWNER')`, user)
	t.Run("human membership cannot cross document workspace", func(t *testing.T) { request(t, serviceSecret, "document-security-foreign", 403) })
	t.Run("private routes absent from public mux", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/internal/docs/v1/"+page+"/state", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Workspace-ID", workspace)
		req.Header.Set("X-Crewship-Docs-Service", serviceSecret)
		_, pattern := router.mux.Handler(req)
		if strings.Contains(pattern, "internal/docs") {
			t.Fatalf("private authority registered publicly: %q", pattern)
		}
		recorder := httptest.NewRecorder()
		router.mux.ServeHTTP(recorder, req)
		if recorder.Code != 404 {
			t.Fatalf("public mux private operation=%d want404", recorder.Code)
		}
	})
}
