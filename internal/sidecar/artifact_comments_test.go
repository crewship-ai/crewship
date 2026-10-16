package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactCommentsTransportActualRunAndReadReplyOnly(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	body := `{"body":"reply","base_version":1,"mutation_id":"same"}`
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "crew-ipc" {
			t.Error("actual run not forwarded")
		}
		if r.Method == "GET" {
			if r.URL.Path != "/api/v1/internal/artifacts/report/threads" || r.URL.Query().Get("limit") != "2" {
				t.Error(r.URL.String())
			}
		} else {
			raw, _ := io.ReadAll(r.Body)
			if r.URL.Path != "/api/v1/internal/artifacts/report/threads/thread/comments" || string(raw) != body {
				t.Error("reply changed")
			}
		}
		w.Header().Set("Retry-After", "5")
		_, _ = w.Write([]byte(`{"threads":[]}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "crew-ipc"
	for _, method := range []string{"GET", "POST"} {
		path := "/artifacts/report/threads?limit=2"
		input := ""
		if method == "POST" {
			path = "/artifacts/report/threads/thread/comments"
			input = body
		}
		r := httptest.NewRequest(method, path, strings.NewReader(input))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleArtifactComments(w, r)
		if w.Code != 200 || w.Header().Get("Retry-After") != "5" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/artifacts/report/threads/thread/resolve", "/artifacts/report/threads"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleArtifactComments(w, r)
		if w.Code != 400 {
			t.Fatal("unsupported operation accepted")
		}
	}
	r := httptest.NewRequest("GET", "/artifacts/report/threads", nil)
	w := httptest.NewRecorder()
	s.handleArtifactComments(w, r)
	if w.Code != 403 || calls != 2 {
		t.Fatal("unauthenticated data forwarded")
	}
}
