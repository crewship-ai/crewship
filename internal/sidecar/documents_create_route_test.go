package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestDocumentCreateRegisteredRoute(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/api/v1/internal/documents/create" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-registered-crew-ipc" || string(body) != `{"mutation_id":"registered-one"}` {
			t.Error("registered route changed the protected request")
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"page_id":"registered-document"}`)
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-registered-crew-ipc"
	handler := s.buildHandler(nil)
	for _, tc := range []struct {
		name, bearer string
		want         int
	}{
		{"missing capability", "", 403},
		{"legacy capability", s.ipc.AgentToken, 403},
		{"other crew", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(runTestMaster, "ws-1", "other-crew"), "ws-1", "agent-boot", "run-boot"), 403},
		{"original live capability", token, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://127.0.0.1:9119/api/v1/documents/create", strings.NewReader(`{"mutation_id":"registered-one"}`))
			request.RemoteAddr = "127.0.0.1:12345"
			if tc.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("registered route status%d want%d", response.Code, tc.want)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("denied capability reached host: calls%d", calls)
	}
}
