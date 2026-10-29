package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func documentRunTransportFixture(t *testing.T, agent, run string, active bool) (*Server, string, *atomic.Int64) {
	t.Helper()
	t.Setenv(runAuthorityEnv, "1")
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", agent, run)
	var calls atomic.Int64
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/internal/runs/"+run+"/status" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": run, "active": active})
			return
		}
		calls.Add(1)
		ws, gotAgent, gotRun, valid := internaltoken.ValidateAgentRunToken(key, bearerToken(r))
		if !valid || ws != "ws-1" || gotAgent != agent || gotRun != run || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("Document host transport did not preserve the verified crew/run binding")
			w.WriteHeader(403)
			return
		}
		wantSlug := map[string]string{"agent-boot": "boot", "agent-peer": "peer"}[agent]
		if r.Header.Get(actingAgentSlugHeader) != wantSlug {
			t.Errorf("untrusted or borrowed acting slug: got %q, want %q", r.Header.Get(actingAgentSlugHeader), wantSlug)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/internal/documents/create" {
			w.WriteHeader(201)
		}
		_, _ = io.WriteString(w, `{"page_id":"saved-document","revision":0}`)
	}))
	t.Cleanup(host.Close)
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
	return s, token, &calls
}

func invokeDocumentRunTransport(t *testing.T, s *Server, token, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1:9119"+path, nil)
	r.RemoteAddr = "127.0.0.1:12345"
	if method == "POST" {
		r.Body = io.NopCloser(strings.NewReader(`{"mutation_id":"same-document-operation"}`))
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set(actingAgentSlugHeader, "caller-forged-boot-slug")
	w := httptest.NewRecorder()
	s.buildHandler(nil).ServeHTTP(w, r)
	return w
}

func TestDocumentTransportAdmitsNewCrewAgents(t *testing.T) {
	for _, agent := range []string{"agent-boot", "agent-peer", "agent-added-after-boot"} {
		t.Run(agent, func(t *testing.T) {
			s, token, calls := documentRunTransportFixture(t, agent, "run-"+agent, true)
			for _, route := range []struct {
				method, path string
				want         int
			}{
				{"POST", "/api/v1/documents/create", 201},
				{"GET", "/api/v1/documents/draft", 200},
				{"POST", "/api/v1/documents/draft", 200},
			} {
				before := calls.Load()
				w := invokeDocumentRunTransport(t, s, token, route.method, route.path)
				if w.Code != route.want || calls.Load() != before+1 {
					t.Errorf("%s %s: status%d host_calls%d, want%d and one host call: %s", route.method, route.path, w.Code, calls.Load()-before, route.want, w.Body.String())
				}
			}
			// Memory still requires its existing token-resolved slug. Document
			// capability forwarding must not relax that separate transport contract.
			if agent == "agent-added-after-boot" {
				r := httptest.NewRequest("POST", "/memory", nil)
				r.Header.Set("Authorization", "Bearer "+token)
				before := calls.Load()
				_, err := s.memoryHostRequest(r, "POST", "/memory-fixture", nil)
				if err == nil || memoryHostRequestReachedHost(err) || calls.Load() != before {
					t.Fatal("Document forwarding changed the memory identity gate")
				}
			}
		})
	}
}

func TestDocumentTransportRejectsInvalidRunCapabilities(t *testing.T) {
	for _, name := range []string{"missing", "legacy boot", "legacy peer", "forged", "wrong crew", "wrong workspace", "ended locally", "inactive authority"} {
		t.Run(name, func(t *testing.T) {
			run := "run-denied"
			s, token, calls := documentRunTransportFixture(t, "agent-added-after-boot", run, name != "inactive authority")
			switch name {
			case "missing":
				token = ""
			case "legacy boot":
				token = s.ipc.AgentToken
			case "legacy peer":
				token = s.crewMembers[0].AuthToken
			case "forged":
				token = "agtv2.forged"
			case "wrong crew":
				token = internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(runTestMaster, "ws-1", "other-crew"), "ws-1", "agent-added-after-boot", run)
			case "wrong workspace":
				token = internaltoken.DeriveAgentRunToken(s.ipc.AgentRunKey, "other-workspace", "agent-added-after-boot", run)
			case "ended locally":
				s.runs.end(run)
			}
			for _, route := range []struct{ method, path string }{{"POST", "/api/v1/documents/create"}, {"GET", "/api/v1/documents/draft"}, {"POST", "/api/v1/documents/draft"}} {
				w := invokeDocumentRunTransport(t, s, token, route.method, route.path)
				if w.Code != 403 || calls.Load() != 0 {
					t.Fatalf("%s %s: status%d host_calls%d, want403 before forwarding", route.method, route.path, w.Code, calls.Load())
				}
			}
		})
	}
}

func TestDocumentRunTransportCannotForwardOtherRoutes(t *testing.T) {
	s, token, calls := documentRunTransportFixture(t, "agent-added-after-boot", "run-closed-transport", true)
	r := httptest.NewRequest("POST", "/document", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	for _, route := range []struct{ method, path string }{{"POST", "/api/v1/internal/memory/write"}, {"DELETE", "/api/v1/internal/documents/draft"}, {"GET", "/api/v1/internal/documents/create"}} {
		_, err := s.documentHostRequest(r, route.method, route.path, nil, 64<<10)
		if err == nil || memoryHostRequestReachedHost(err) || calls.Load() != 0 {
			t.Fatalf("unsupported transport reached host: %s %s", route.method, route.path)
		}
	}
}
