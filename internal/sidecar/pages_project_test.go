package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageProjectMCPIdentityAndBoundedRequest(t *testing.T) {
	calls := 0
	wantedAgent := "agent-real"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/internal/pages/project" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["workspace_id"] != "ws-real" || body["crew_id"] != "crew-real" || body["agent_id"] != wantedAgent {
			t.Errorf("forged actor: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"revision":1,"state":"draft"}`))
	}))
	defer upstream.Close()
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: upstream.URL, Token: "internal-test", WorkspaceID: "ws-real", CrewID: "crew-real", AgentID: "agent-real", AgentToken: "boot-test-token"})
	s.crewMembers = []CrewMember{{ID: "agent-peer", Slug: "peer", AuthToken: "peer-test-token"}}
	bearer := "boot-test-token"
	call := func(arguments string) *httptest.ResponseRecorder {
		request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"page_project","arguments":` + arguments + `}}`
		r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(request))
		r.Host = "127.0.0.1:9119"
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		s.handleRoutinesMCP(w, r)
		return w
	}
	w := call(`{"operation":"init","slug":"health","expected_revision":0}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "revision") {
		t.Fatal(w.Body.String())
	}
	w = call(`{"operation":"init","slug":"health","agent_id":"forged"}`)
	if !strings.Contains(w.Body.String(), "invalid project arguments") {
		t.Fatal(w.Body.String())
	}
	w = call(`{"operation":"save","slug":"health","files":[{"path":"x","encoding":"utf8","content":"` + strings.Repeat("x", 1<<20) + `"}]}`)
	if w.Code != 400 {
		t.Fatalf("oversized tool call %d", w.Code)
	}
	for _, missingOrForged := range []string{"", "forged"} {
		bearer = missingOrForged
		if w := call(`{"operation":"read","slug":"health"}`); !strings.Contains(w.Body.String(), "unrecognized agent token") {
			t.Fatal(w.Body.String())
		}
	}
	bearer = "peer-test-token"
	wantedAgent = "agent-peer"
	if w := call(`{"operation":"read","slug":"health"}`); !strings.Contains(w.Body.String(), "revision") {
		t.Fatal(w.Body.String())
	}
	if calls != 2 {
		t.Fatalf("invalid requests forwarded %d times", calls)
	}
}
