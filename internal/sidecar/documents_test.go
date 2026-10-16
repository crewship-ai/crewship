package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestDocumentSidecarPreservesRunAndDeniesOtherOperations(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.URL.Path != "/api/v1/internal/documents/report/patch" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("lost document path or original run bearer")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"id":"stable","target":"block"}` {
			t.Errorf("body=%s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rev":2}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	side := httptest.NewServer(s.buildHandler(NewProxy(ProxyConfig{})))
	defer side.Close()
	for _, tc := range []struct {
		suffix, bearer string
		want           int
	}{{"patch", token, 200}, {"restore", token, 403}, {"requests", token, 403}, {"patch", token + "bad", 403}} {
		req, _ := http.NewRequest("POST", side.URL+"/api/v1/pages/report/document/"+tc.suffix, strings.NewReader(`{"id":"stable","target":"block"}`))
		req.Header.Set("Authorization", "Bearer "+tc.bearer)
		response, err := side.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("%s=%d want%d", tc.suffix, response.StatusCode, tc.want)
		}
	}
	if calls != 1 {
		t.Fatalf("unauthorized operations forwarded: %d", calls)
	}
}

func TestDocumentSidecarForwardsOnlyOneValidatedCurrentBlockSelector(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	queries := []string{}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		queries = append(queries, r.URL.RawQuery)
		if r.Method != "GET" || r.URL.Path != "/api/v1/internal/documents/report" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected forwarded read: %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"page_id":"page-original","block":{"id":"target_1","text":"Current"}}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	side := httptest.NewServer(s.buildHandler(NewProxy(ProxyConfig{})))
	defer side.Close()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/document?block_id=target_1", 200},
		{"GET", "/document?block_id=target_1&workspace_id=ws-1", 200},
		{"GET", "/document?workspace_id=ws-1", 200},
		{"GET", "/document?block_id=", 400},
		{"GET", "/document?block_id=a&block_id=b", 400},
		{"GET", "/document?block_id=..%2Fother", 400},
		{"GET", "/document?block_id=a&sinceRev=1", 400},
		{"GET", "/document?workspace_id=other", 400},
		{"POST", "/document/patch?block_id=target_1", 400},
		{"GET", "/document/comments?block_id=target_1", 403},
	} {
		req, _ := http.NewRequest(tc.method, side.URL+"/api/v1/pages/report"+tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := side.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("%s %s=%d want%d", tc.method, tc.path, response.StatusCode, tc.want)
		}
	}
	if calls != 3 || strings.Join(queries, "|") != "block_id=target_1|block_id=target_1|" {
		t.Fatalf("invalid selectors reached host %d times", calls)
	}
}
