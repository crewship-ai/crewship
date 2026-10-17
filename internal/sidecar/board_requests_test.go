package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestBoardRequestTransportScopesAndActualRunCredentials(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "crew-ipc" {
			t.Error("lost actual transport credentials")
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/internal/board-requests/request/") {
			t.Error(r.URL.Path)
		}
		w.Header().Set("Retry-After", "5")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "crew-ipc"
	for _, path := range []string{"/board-requests/request/mermaid", "/board-requests/request/files/allowed", "/board-requests/request/sources/allowed"} {
		method := "GET"
		if strings.HasSuffix(path, "mermaid") {
			method = "POST"
		}
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleBoardRequest(w, r)
		if w.Code != 200 || w.Header().Get("Retry-After") != "5" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/board-requests/request/mermaid", strings.NewReader(strings.Repeat("x", 65537)))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleBoardRequest(w, r)
	if w.Code != 413 || calls != 3 {
		t.Fatal("unbounded source forwarded")
	}
	r = httptest.NewRequest("GET", "/board-requests/request/files/allowed", nil)
	w = httptest.NewRecorder()
	s.handleBoardRequest(w, r)
	if w.Code != 403 || calls != 3 {
		t.Fatal("missing capability forwarded")
	}
}
