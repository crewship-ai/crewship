package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestMooringMCPUsesOnlyOriginalRunAndFixedHostToolPaths(t *testing.T) {
	s, key := newRunIdentityServer(t)
	s.ipc.Token = "synthetic-document-ipc"
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	original := `{"type":"submit_changes","request_id":"synthetic-original","changes":[]}`
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.URL.Path != "/api/v1/internal/mooring/task" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") == "" {
			t.Error("host transport identity or route changed")
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost && string(body) != original {
			t.Error("original submit bytes changed")
		}
		_, _ = io.WriteString(w, `{"synthetic":"host response"}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	call := func(body, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp/mooring", strings.NewReader(body))
		r.Host = "127.0.0.1:9119"
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		s.handleMooringMCP(w, r)
		return w
	}
	for _, request := range []string{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doc_read","arguments":{}}}`, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"doc_submit","arguments":{"submission":` + original + `}}}`} {
		w := call(request, token)
		failed, _ := documentCreateMCPResult(t, w)
		if failed {
			t.Fatal("fixed run transport refused")
		}
	}
	if calls != 2 {
		t.Fatal("missing fixed host operations")
	}
	for _, args := range []string{`{"name":"doc_read","arguments":{"page_id":"other"}}`, `{"name":"doc_submit","arguments":{"submission":{},"agent_id":"other"}}`, `{"name":"doc_read","name":"doc_submit","arguments":{}}`, `{"name":"doc_draft_apply","arguments":{}}`} {
		w := call(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":`+args+`}`, token)
		if w.Code == http.StatusOK {
			failed, _ := documentCreateMCPResult(t, w)
			if !failed {
				t.Fatal("unsupported/ambiguous tool accepted")
			}
		}
	}
	if calls != 2 {
		t.Fatal("invalid tool reached host")
	}
	for _, credential := range []string{"", "agentv1.synthetic", "agtv2.invalid"} {
		if call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, credential).Code != 403 {
			t.Fatal("unbound run obtained document tools")
		}
	}
}
