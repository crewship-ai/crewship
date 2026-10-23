package sidecar

import (
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	arguments := `{"artifact_id":"page","mandate_id":"m","request":{"mutation_id":"original-edit","mandate_id":"m","base_rev":1,"generation":1,"read_set":{"cells":[{"sheet":"Sheet1","cell":"A1","rev":1}],"ranges":[{"sheet":"Sheet1","start":"A1","end":"A9","rev":1,"structure_rev":1}]},"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`
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
	if len(bodies) != 2 || bodies[0] != bodies[1] || !strings.Contains(bodies[0], `"base_rev":1`) || !strings.Contains(bodies[0], `"mutation_id":"original-edit"`) || !strings.Contains(bodies[0], `"read_set":{"cells":[{"sheet":"Sheet1","cell":"A1","rev":1}],"ranges":[{"sheet":"Sheet1","start":"A1","end":"A9","rev":1,"structure_rev":1}]}`) {
		t.Fatalf("request was renewed: %v", bodies)
	}
}

func TestGridCraftHostReceiptCanOutliveMemoryIPCBudget(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10100 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"mutation_id":"retained","rev":2}`))
	}))
	defer host.Close()
	s := newRoutineMCPTestServer(t, &IPCConfig{GridCraftEnabled: true, BaseURL: host.URL, Token: "fixture", WorkspaceID: "ws"})
	r := httptest.NewRequest("POST", "/", nil)
	response, err := s.sendBoundedHostRequest(r, "POST", "/api/v1/internal/gridcraft", []byte(`{}`), 1024, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if response.status != 200 || !strings.Contains(string(response.body), `"mutation_id":"retained"`) {
		t.Fatalf("receipt lost: %#v", response)
	}
}

// workbook_commits forwards the agent's commits feed request under its signed
// run, unchanged, and refuses packets that mix in other operations.
func TestGridCraftMCPCommitsFeed(t *testing.T) {
	s, key := newRunIdentityServer(t)
	s.ipc.GridCraftEnabled = true
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var bodies []string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/gridcraft" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("workbook transport changed verified authority")
			w.WriteHeader(403)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"head_rev":3,"generation":1,"min_since":0,"commits":[{"rev":3,"kind":"cells","actor_label":"Owner","own":false,"ops":[{"sheet":"Sheet1","cell":"C3","input":"9"}]}]}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "synthetic-workbook-ipc"
	var listed bool
	for _, tool := range gridcraftMCPCatalog() {
		if tool.Name == "workbook_commits" {
			listed = strings.Contains(string(tool.InputSchema), `"since"`) && strings.Contains(string(tool.InputSchema), `"limit"`)
		}
	}
	if !listed {
		t.Fatal("workbook_commits is not offered with since and limit")
	}
	call := func(arguments string) (bool, string) {
		r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workbook_commits","arguments":`+arguments+`}}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleRoutinesMCP(w, r)
		return documentCreateMCPResult(t, w)
	}
	if failed, body := call(`{"artifact_id":"page","mandate_id":"m","since":2,"limit":50}`); failed || !strings.Contains(body, `"head_rev":3`) {
		t.Fatalf("commits failed: %s", body)
	}
	if len(bodies) != 1 || bodies[0] != `{"action":"commits","artifact_id":"page","limit":50,"mandate_id":"m","since":2}` {
		t.Fatalf("forwarded %v", bodies)
	}
	if failed, body := call(`{"artifact_id":"page","mandate_id":"m","since":0}`); failed || len(bodies) != 2 || bodies[1] != `{"action":"commits","artifact_id":"page","mandate_id":"m","since":0}` {
		t.Fatalf("since 0: %s %v", body, bodies)
	}
	for _, bad := range []string{
		`{"artifact_id":"page","mandate_id":"m"}`,
		`{"artifact_id":"page","mandate_id":"m","since":0,"mutation_id":"x"}`,
		`{"artifact_id":"page","mandate_id":"m","since":-1}`,
		`{"artifact_id":"page","mandate_id":"m","since":0,"limit":201}`,
	} {
		if failed, _ := call(bad); !failed {
			t.Errorf("accepted %s", bad)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("invalid packets reached the host: %v", bodies)
	}
	// Feed fields belong only to commits.
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workbook_read","arguments":{"artifact_id":"page","mandate_id":"m","since":0}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	if failed, _ := documentCreateMCPResult(t, w); !failed || len(bodies) != 2 {
		t.Fatal("read accepted feed fields")
	}
}
