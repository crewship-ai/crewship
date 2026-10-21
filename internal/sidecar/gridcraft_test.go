package sidecar

import (
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGridCraftSurfaceDefaultOffAndRefusesLegacyCredential(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := newRoutineMCPTestServer(t, &IPCConfig{GridCraftEnabled: enabled, BaseURL: "http://must-not-be-called", Token: "fixture", WorkspaceID: "ws"})
		r := httptest.NewRequest(http.MethodPost, "/api/v1/gridcraft", strings.NewReader(`{"action":"read","artifact_id":"page","mandate_id":"m"}`))
		r.Header.Set("Authorization", "Bearer legacy-token")
		w := httptest.NewRecorder()
		s.handleGridCraft(w, r)
		want := 404
		if enabled {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("enabled=%v status=%d body=%s", enabled, w.Code, w.Body)
		}
	}
}

func TestGridCraftMCPKeepsSignedRunAndOriginalMutationOnRetry(t *testing.T) {
	s, key := newRunIdentityServer(t)
	s.ipc.GridCraftEnabled = true
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var bodies []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/gridcraft" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-workbook-ipc" {
			t.Error("workbook transport changed verified authority")
			w.WriteHeader(403)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"operation_key":{"mutation_id":"original-edit"},"rev":2,"durability":"node"}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "synthetic-workbook-ipc"
	arguments := `{"artifact_id":"page","mandate_id":"m","request":{"mutation_id":"original-edit","mandate_id":"m","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workbook_apply","arguments":`+arguments+`}}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleRoutinesMCP(w, r)
		failed, body := documentCreateMCPResult(t, w)
		if failed {
			t.Fatalf("workbook MCP failed: %s", body)
		}
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || !strings.Contains(bodies[0], `"base_rev":1`) || !strings.Contains(bodies[0], `"mutation_id":"original-edit"`) {
		t.Fatalf("request was renewed: %v", bodies)
	}
}
