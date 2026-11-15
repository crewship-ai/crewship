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

// #5317 step 5.3 (coordinator decision on #5355): a chat run reads a Word
// document, a Sheet or a Mooring document it reaches through its run scope.
// The sidecar only carries the selection; the server is the only authority.

type scopeReadHost struct {
	paths, queries, bodies []string
	status                 int
	body                   string
}

func newScopeReadRig(t *testing.T) (*Server, string, *scopeReadHost) {
	t.Helper()
	s, key := newRunIdentityServer(t)
	s.ipc.GridCraftEnabled = true
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	rec := &scopeReadHost{status: 200, body: `{"page_id":"own-page"}`}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		rec.paths = append(rec.paths, r.URL.Path)
		rec.queries = append(rec.queries, r.URL.RawQuery)
		rec.bodies = append(rec.bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rec.status)
		_, _ = io.WriteString(w, rec.body)
	}))
	t.Cleanup(host.Close)
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "synthetic-crew-ipc"
	return s, token, rec
}

// The draft view forwards one page_id, and only when it is the single query
// parameter; the host's answer (here a refusal) is passed through unchanged.
func TestDocumentDraftViewForwardsOnlyASinglePageID(t *testing.T) {
	s, token, host := newScopeReadRig(t)
	invoke := func(method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleDocumentDraft(w, r)
		return w
	}
	host.status, host.body = 403, `{"error":"document access denied"}`
	w := invoke("GET", "/api/v1/documents/draft?page_id=foreign")
	if len(host.queries) != 1 || host.paths[0] != "/api/v1/internal/documents/draft" || host.queries[0] != "page_id=foreign" {
		t.Fatalf("page_id not forwarded alone: %v %v", host.paths, host.queries)
	}
	if w.Code != 403 || w.Body.String() != host.body {
		t.Fatalf("the server's refusal was changed: %d %s", w.Code, w.Body)
	}
	for _, target := range []string{
		"/api/v1/documents/draft?page_id=a&page_id=b",
		"/api/v1/documents/draft?page_id=a&workspace_id=ws-1",
		"/api/v1/documents/draft?workspace_id=ws-1",
		"/api/v1/documents/draft?page_id=",
		"/api/v1/documents/draft?page_id=" + strings.Repeat("a", 257),
		"/api/v1/documents/draft?page_id=a;b=c",
	} {
		if w := invoke("GET", target); w.Code != 400 {
			t.Fatalf("%s: %d %s", target, w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/documents/draft?page_id=a", strings.NewReader(`{"operation":"checkpoint","generation":1,"request":{}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.handleDocumentDraft(w, r)
	if w.Code != 400 {
		t.Fatalf("a write carried a page_id: %d", w.Code)
	}
	if len(host.queries) != 1 {
		t.Fatalf("refused requests reached the host: %v", host.queries)
	}
}

// The host transport itself carries a query only as the draft view's single
// page_id, whatever a future caller builds.
func TestDocumentHostTransportCarriesOnlyASinglePageID(t *testing.T) {
	s, token, host := newScopeReadRig(t)
	r := httptest.NewRequest("GET", "/api/v1/documents/draft", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/internal/documents/draft?page_id=a&workspace_id=ws-1"},
		{"GET", "/api/v1/internal/documents/draft?workspace_id=ws-1"},
		{"POST", "/api/v1/internal/documents/draft?page_id=a"},
		{"POST", "/api/v1/internal/documents/create?page_id=a"},
	} {
		if _, err := s.documentHostRequest(r, c.method, c.path, nil, 64<<10); err == nil {
			t.Fatalf("%s %s was carried", c.method, c.path)
		}
	}
	if len(host.queries) != 0 {
		t.Fatalf("refused transports reached the host: %v", host.queries)
	}
	if _, err := s.documentHostRequest(r, "GET", "/api/v1/internal/documents/draft?page_id=a", nil, 64<<10); err != nil || len(host.queries) != 1 || host.queries[0] != "page_id=a" {
		t.Fatalf("single page_id not carried: %v %v", err, host.queries)
	}
}

func scopeMCP(t *testing.T, s *Server, token, name, arguments string) (bool, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+arguments+`}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	return documentCreateMCPResult(t, w)
}

func requiredOf(t *testing.T, tools []memoryMCPToolDescriptor, name string) []string {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			var schema struct {
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			return schema.Required
		}
	}
	t.Fatalf("tool %s not listed", name)
	return nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// mandate_id is optional only on reads; every write still needs it and a
// write without one never reaches the host (so it never moves a head).
func TestRunScopeMCPReadsNeedNoMandateWritesDo(t *testing.T) {
	s, token, host := newScopeReadRig(t)
	if contains(requiredOf(t, gridcraftMCPCatalog(), "workbook_read"), "mandate_id") {
		t.Fatal("workbook_read still requires mandate_id")
	}
	for _, name := range []string{"workbook_apply", "workbook_status", "workbook_commits"} {
		if !contains(requiredOf(t, gridcraftMCPCatalog(), name), "mandate_id") {
			t.Fatalf("%s no longer requires mandate_id", name)
		}
	}
	if contains(requiredOf(t, mooringMCPCatalog(), "mooring_command"), "mandate_id") {
		t.Fatal("mooring_command read still requires mandate_id")
	}
	if contains(requiredOf(t, documentDraftMCPCatalog(), "doc_draft_view"), "page_id") {
		t.Fatal("doc_draft_view requires page_id")
	}

	if failed, body := scopeMCP(t, s, token, "workbook_read", `{"artifact_id":"page"}`); failed {
		t.Fatalf("workbook_read without a mandate: %s", body)
	}
	if len(host.bodies) != 1 || host.bodies[0] != `{"action":"read","artifact_id":"page"}` {
		t.Fatalf("forwarded %v", host.bodies)
	}
	if failed, body := scopeMCP(t, s, token, "mooring_command", `{"action":"read","artifact_id":"page"}`); failed {
		t.Fatalf("mooring read without a mandate: %s", body)
	}
	if len(host.bodies) != 2 || host.bodies[1] != `{"action":"read","artifact_id":"page"}` {
		t.Fatalf("forwarded %v", host.bodies)
	}
	if failed, body := scopeMCP(t, s, token, "doc_draft_view", `{"page_id":"attached"}`); failed {
		t.Fatalf("doc_draft_view with a page: %s", body)
	}
	if len(host.queries) != 3 || host.queries[2] != "page_id=attached" {
		t.Fatalf("view forwarded %v", host.queries)
	}
	if failed, _ := scopeMCP(t, s, token, "doc_draft_view", `{}`); failed || len(host.queries) != 4 || host.queries[3] != "" {
		t.Fatalf("plain view changed: %v", host.queries)
	}
	for _, write := range []struct{ name, args string }{
		{"workbook_apply", `{"artifact_id":"page","request":{"mutation_id":"m1","base_rev":1,"generation":1,"command":{"kind":"cell.set","sheet":"Sheet1","cell":"B2","input":"42"}}}`},
		{"workbook_status", `{"artifact_id":"page","mutation_id":"m1"}`},
		{"workbook_commits", `{"artifact_id":"page","since":0}`},
		{"mooring_command", `{"action":"command","artifact_id":"page","request":{"mutation_id":"m1","base_rev":1,"generation":1,"command":"set","arguments":{"text":"x"}}}`},
		{"doc_draft_view", `{"page_id":"attached","mandate_id":"x"}`},
		{"doc_draft_view", `{"page_id":""}`},
	} {
		if failed, body := scopeMCP(t, s, token, write.name, write.args); !failed {
			t.Fatalf("%s %s accepted: %s", write.name, write.args, body)
		}
	}
	if len(host.bodies) != 4 {
		t.Fatalf("a write without a mandate reached the host: %v", host.bodies)
	}
}
