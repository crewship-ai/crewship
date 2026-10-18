package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestDocumentCreateWorkspaceTransport(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || r.URL.Path != "/api/v1/internal/documents/create" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" || body["mutation_id"] != "create-one" || len(body["blocks"].([]any)) != 1 {
			t.Fatalf("incorrect Docs creation transport: %s %s", r.URL, raw)
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"page_id":"saved-doc","revision":0}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "synthetic-crew-ipc"
	body := `{"slug":"doc","name":"Report","mutation_id":"create-one","blocks":[{"type":"paragraph","text":"finished"}]}`
	request := httptest.NewRequest("POST", "/api/v1/documents/create", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	s.handleDocumentCreate(response, request)
	if calls != 1 || response.Code != 201 || !strings.Contains(response.Body.String(), "saved-doc") {
		t.Fatalf("direct creation: calls%d %s", calls, response.Body.String())
	}
	for _, query := range []string{"?workspace_id=ws-1", "?workspace_id=wrong", "?workspace_id=ws-1&workspace_id=ws-1", "?block_id=leaf"} {
		r := httptest.NewRequest("POST", "/api/v1/documents/create"+query, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		s.handleDocumentCreate(rr, r)
		want := 400
		if query == "?workspace_id=ws-1" {
			want = 201
		}
		if rr.Code != want {
			t.Fatalf("query%s: %d %s", query, rr.Code, rr.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("invalid query reached host: %d", calls)
	}
}

func TestDocumentCreateBoundsAndDeniedIdentity(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	responseBody := `{"page_id":"created"}`
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		_, _ = io.WriteString(w, responseBody)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	for _, test := range []struct {
		name, capability, body string
		want                   int
	}{
		{"missing token", "", `{}`, 403},
		{"legacy token", s.ipc.AgentToken, `{}`, 403},
		{"wrong crew", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(runTestMaster, "ws-1", "other-crew"), "ws-1", "agent-boot", "run-boot"), `{}`, 403},
		{"wrong workspace", internaltoken.DeriveAgentRunToken(key, "other-ws", "agent-boot", "run-boot"), `{}`, 403},
		{"oversized request", token, strings.Repeat("x", (1<<20)+1), 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v1/documents/create", strings.NewReader(test.body))
			r.Header.Set("Authorization", "Bearer "+test.capability)
			rr := httptest.NewRecorder()
			s.handleDocumentCreate(rr, r)
			if rr.Code != test.want || calls != 0 {
				t.Fatalf("status%d calls%d: %s", rr.Code, calls, rr.Body.String())
			}
		})
	}
	responseBody = strings.Repeat("x", (64<<10)+1)
	r := httptest.NewRequest("POST", "/api/v1/documents/create", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	s.handleDocumentCreate(rr, r)
	if rr.Code != 503 || calls != 1 || !strings.Contains(rr.Body.String(), "same mutation_id") || strings.Contains(rr.Body.String(), "xxxx") {
		t.Fatalf("unknown sent outcome must not expose a partial response: %d calls%d %s", rr.Code, calls, rr.Body.String())
	}
	// The shared memory transport keeps its original 10 MiB allowance.
	responseBody = strings.Repeat("m", 65<<10)
	response, err := s.memoryHostRequest(r, http.MethodPost, "/memory-fixture", nil)
	if err != nil || len(response.body) != 65<<10 {
		t.Fatalf("memory allowance changed: %v", err)
	}
}
