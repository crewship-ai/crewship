package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// MCP request metadata belongs to the transport envelope, never the command.
func TestMooringMCPAcceptsProgressMetadataWithoutChangingHostOperation(t *testing.T) {
	s, key := newRunIdentityServer(t)
	s.ipc.Token = "synthetic-document-ipc"
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	original := `{"type":"submit_changes","request_id":"synthetic-original","changes":[]}`
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		calls++
		if r.URL.Path != "/api/v1/internal/mooring/task" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != s.ipc.Token {
			t.Error("transport identity or fixed route changed")
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost && string(body) != original {
			t.Error("metadata changed the original submission")
		}
		if r.Method == http.MethodGet && len(body) != 0 {
			t.Error("metadata reached the document read")
		}
		_, _ = io.WriteString(w, `{"synthetic":"host response"}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	call := func(params string) bool {
		r := httptest.NewRequest(http.MethodPost, "/mcp/mooring", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+params+`}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleMooringMCP(w, r)
		if w.Code == http.StatusBadRequest {
			return false // Duplicate fields and oversized envelopes fail before dispatch.
		}
		failed, _ := documentCreateMCPResult(t, w)
		return !failed
	}
	for _, meta := range []string{"", `,"_meta":{"progressToken":7}`, `,"_meta":{"progressToken":"synthetic-progress"}`} {
		for _, operation := range []string{`"name":"doc_read","arguments":{}`, `"name":"doc_submit","arguments":{"submission":` + original + `}`} {
			if !call(`{` + operation + meta + `}`) {
				t.Fatal("valid MCP operation with optional progress metadata refused")
			}
		}
	}
	if calls != 6 {
		t.Fatalf("host received %d operations, want 6", calls)
	}
	for _, params := range []string{
		`{"name":"doc_read","arguments":{},"unknown":true}`,
		`{"name":"doc_read","arguments":{"page_id":"other"},"_meta":{"progressToken":7}}`,
		`{"name":"doc_read","arguments":{},"_meta":{},"_meta":{}}`,
		`{"name":"doc_read","arguments":{},"_meta":{"progressToken":7,"progressToken":8}}`,
		`{"name":"doc_read","arguments":{},"_meta":"invalid"}`,
		`{"name":"doc_read","arguments":{},"_meta":null}`,
		`{"name":"doc_read","arguments":{},"_meta":[]}`,
		`{"name":"doc_read","arguments":{},"_meta":{"progressToken":"` + strings.Repeat("x", 256<<10) + `"}}`,
	} {
		if call(params) {
			t.Fatal("invalid envelope or document argument accepted")
		}
	}
	if calls != 6 {
		t.Fatal("invalid request reached the host")
	}
}
