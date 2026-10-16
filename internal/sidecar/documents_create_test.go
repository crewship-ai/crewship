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

func TestDocumentCreateMCPAndWorkspaceTransport(t *testing.T) {
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
	response := artifactMCPCall(t, s, token, "doc_create", body)
	if calls != 1 || strings.Contains(response.Body.String(), `"isError":true`) || !strings.Contains(response.Body.String(), "saved-doc") {
		t.Fatalf("MCP creation: calls%d %s", calls, response.Body.String())
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
