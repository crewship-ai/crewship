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
