package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutinesMCPDraftInjectsIdentityAndNeverPublishes(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/internal/pipelines/drafts/save" {
			t.Errorf("unexpected execution/publication path %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["workspace_id"] != "ws" || body["author_crew_id"] != "crew" || body["author_agent_id"] != "agent" {
			t.Error("forged identity", body)
		}
		draft := body["draft"].(map[string]any)
		if draft["revision"] != float64(5) || draft["id"] != "draft1" {
			t.Error("lost CAS", draft)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"draft":{"id":"draft1","revision":6},"editor_url":"/routines?draft=demo&draft_id=draft1","published":false}`))
	}))
	defer upstream.Close()
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: upstream.URL, Token: "test", WorkspaceID: "ws", CrewID: "crew", AgentID: "agent"})
	req := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_routine_draft","arguments":{"slug":"demo","workspace_id":"forged","author_crew_id":"forged","draft":{"id":"draft1","slug":"demo","revision":5,"document":{"slug":"demo","definition":{}}}}}}`))
	req.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, req)
	if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "editor_url") {
		t.Fatalf("response %d %s calls=%d", w.Code, w.Body, calls)
	}
}
