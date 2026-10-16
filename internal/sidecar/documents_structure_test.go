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

func TestDocumentStructureToolsUseOriginalServerContext(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	reads, writes := 0, 0
	denied := false
	var bodies []map[string]any
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("original run capability lost")
		}
		if denied {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":"revoked"}`))
			return
		}
		if r.Method == "GET" {
			reads++
			// A newer current revision/schema must never replace original request base.
			_, _ = w.Write([]byte(`{"page_id":"original-page","schema":2,"rev":99,"epoch":"renewed-epoch","snapshot":"private-binary","updates":["private-update"],"content":{"type":"doc"},"outline":[],"agent_request":{"kind":"structure","request_id":"original-request","page_id":"original-page","schema":3,"base_rev":7}}`))
			return
		}
		writes++
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid forwarded JSON")
		}
		bodies = append(bodies, body)
		if body["schema"] != float64(3) || body["page_id"] != "original-page" || body["epoch"] != "renewed-epoch" {
			t.Errorf("identity changed: %v", body)
		}
		if strings.HasSuffix(r.URL.Path, "/patch") {
			if body["ifRev"] != float64(7) || body["id"] != "original-request-patch" || body["request_id"] != "original-request" {
				t.Errorf("request rebased: %v", body)
			}
		} else if body["ifRev"] != float64(8) || body["id"] != "original-request-checkpoint" {
			t.Errorf("checkpoint rebased: %v", body)
		}
		_, _ = w.Write([]byte(`{"rev":8,"duplicate":true}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	side := httptest.NewServer(s.buildHandler(NewProxy(ProxyConfig{})))
	defer side.Close()
	request := func(operation, body string) (int, string) {
		t.Helper()
		method := "POST"
		if operation == "agent-read" {
			method = "GET"
		}
		r, _ := http.NewRequest(method, side.URL+"/api/v1/pages/report/document/"+operation, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		response, err := side.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(data)
	}
	status, body := request("agent-read", "")
	if status != 200 || strings.Contains(body, "private-") || !strings.Contains(body, "original-request") {
		t.Fatalf("model read leaked binary or lost scope: %d %s", status, body)
	}
	for _, forged := range []string{`{"operations":[{"op":"delete","id":"b"}],"ifRev":99}`, `{"operations":[]}`, `{"operations":[{"op":"delete","id":"b"}],"page_id":"other"}`} {
		if status, _ := request("agent-apply", forged); status != 400 {
			t.Fatalf("model envelope accepted: %d", status)
		}
	}
	if reads != 1 || writes != 0 {
		t.Fatal("forged model envelope reached host")
	}
	if status, _ := request("agent-apply", `{"operations":[{"op":"delete","id":"b"}]}`); status != 200 {
		t.Fatal(status)
	}
	result := artifactMCPCall(t, s, token, "doc_apply", `{"slug":"report","operations":[{"op":"delete","id":"b"}]}`)
	if strings.Contains(result.Body.String(), `"isError":true`) || len(bodies) != 2 {
		t.Fatalf("MCP did not enter same adapter: %s", result.Body.String())
	}
	first, _ := json.Marshal(bodies[0])
	second, _ := json.Marshal(bodies[1])
	if string(first) != string(second) {
		t.Fatal("CLI HTTP and MCP envelope differ")
	}
	if status, _ := request("agent-checkpoint", `{}`); status != 200 {
		t.Fatal(status)
	}
	denied = true
	if status, _ := request("agent-apply", `{"operations":[{"op":"delete","id":"b"}]}`); status != 403 {
		t.Fatal("revoked retry accepted", status)
	}
	if writes != 3 {
		t.Fatal("revoked retry wrote", writes)
	}
}

func TestDocumentStructureToolsRejectMissingOrForeignContext(t *testing.T) {
	for _, context := range []string{`null`, `{"kind":"structure","request_id":"r","page_id":"foreign","schema":3,"base_rev":1}`, `{"kind":"structure","request_id":"r","page_id":"p","schema":4,"base_rev":1}`} {
		s, key := newRunIdentityServer(t)
		token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
		writes := 0
		host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "run-status") {
				w.WriteHeader(503)
				return
			}
			if r.Method != "GET" {
				writes++
			}
			_, _ = w.Write([]byte(`{"page_id":"p","schema":3,"rev":1,"epoch":"e","agent_request":` + context + `}`))
		}))
		s.ipc.BaseURL = host.URL
		result := artifactMCPCall(t, s, token, "doc_checkpoint", `{"slug":"report"}`)
		host.Close()
		if writes != 0 || !strings.Contains(result.Body.String(), `"isError":true`) {
			t.Fatalf("unbound context accepted: %s", result.Body.String())
		}
	}
}

func TestDocumentStructureToolsOperationsBudgetExcludesEnvelope(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	writes := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"page_id":"p","schema":3,"rev":1,"epoch":"e","agent_request":{"kind":"structure","request_id":"r","page_id":"p","schema":3,"base_rev":1}}`))
			return
		}
		writes++
		var body struct {
			Operations json.RawMessage `json:"operations"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Operations) != documentStructureIntentLimit {
			t.Error("operations limit changed after wrapper")
		}
		_, _ = w.Write([]byte(`{"rev":2}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	const prefix = `[{"op":"insert_after","after_id":"a","node":{"type":"paragraph","content":[{"type":"text","text":"`
	const suffix = `"}]}}]`
	for _, extra := range []int{0, 1} {
		operations := prefix + strings.Repeat("x", documentStructureIntentLimit-len(prefix)-len(suffix)+extra) + suffix
		r := httptest.NewRequest("POST", "/api/v1/pages/report/document/agent-apply", strings.NewReader(`{"operations":`+operations+`}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleDocument(w, r)
		want := 200
		if extra == 1 {
			want = 413
		}
		if w.Code != want {
			t.Fatalf("operations bytes %d: %d %s", len(operations), w.Code, w.Body.String())
		}
	}
	if writes != 1 {
		t.Fatal("oversized operations reached writer", writes)
	}
}
