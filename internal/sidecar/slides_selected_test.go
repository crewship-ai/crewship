package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestSelectedSlidesSidecarActualRoutingOriginalBearerAndAllowlist(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		wantPath := "/api/v1/internal/slides/document-deck/requests/selected_request"
		if r.Method == http.MethodPost {
			wantPath += "/replace"
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"page_id":"immutable","request_schema_version":1,"request_id":"selected_request","replacement":"Česky"}` {
				t.Error("replacement body changed", string(body))
			}
		}
		if r.URL.Path != wantPath || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("lost original run bearer or strict selected path", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"revision":2}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	side := httptest.NewServer(s.buildHandler(NewProxy(ProxyConfig{})))
	defer side.Close()
	for _, tc := range []struct {
		method, path, bearer, body string
		want                       int
	}{
		{"GET", "/api/v1/pages/document-deck/slides/requests/selected_request", token, "", 200},
		{"POST", "/api/v1/pages/document-deck/slides/requests/selected_request/replace", token, `{"page_id":"immutable","request_schema_version":1,"request_id":"selected_request","replacement":"Česky"}`, 200},
		{"POST", "/api/v1/pages/document-deck/slides/patch", token, `{}`, 400},
		{"POST", "/api/v1/pages/document-deck/slides/requests/selected_request/cancel", token, `{}`, 403},
		{"GET", "/api/v1/pages/document-deck/slides/requests/selected_request?refresh=1", token, "", 400},
		{"GET", "/api/v1/pages/document-deck/slides/requests/selected_request", token + "bad", "", 403},
		{"GET", "/api/v1/pages/document-deck/slides/requests/%2e%2e", token, "", 400},
		{"POST", "/api/v1/pages/document-deck/slides/requests/selected_request/replace", token, strings.Repeat("x", (256<<10)+1), 413},
	} {
		req, err := http.NewRequest(tc.method, side.URL+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tc.bearer)
		response, err := side.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("%s %s=%d want%d", tc.method, tc.path, response.StatusCode, tc.want)
		}
	}
	if calls != 2 {
		t.Fatal("unauthorized forwarding", calls)
	}
}
