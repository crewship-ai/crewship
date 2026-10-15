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

func TestArtifactRequestUpdateHTTPRouteDoesNotFallThroughToProxy(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/internal/artifact-requests/job/updates" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected forwarded route or principal")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"mutation_id":"one","operation":"reply","body":"Done"}` {
			t.Errorf("forwarded body=%s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"job","status":"succeeded","result_kind":"reply"}`))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	side := httptest.NewServer(s.buildHandler(NewProxy(ProxyConfig{})))
	defer side.Close()
	call := func(bearer string) (int, string) {
		t.Helper()
		req, err := http.NewRequest("POST", side.URL+"/artifact-requests/job/updates", strings.NewReader(`{"mutation_id":"one","operation":"reply","body":"Done"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		response, err := side.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(raw)
	}
	status, body := call(token)
	if status != 200 || calls != 1 || !json.Valid([]byte(body)) || body != `{"id":"job","status":"succeeded","result_kind":"reply"}` {
		t.Fatalf("route double handled: status=%d calls=%d body=%q", status, calls, body)
	}
	status, body = call(token + "invalid")
	if status != 403 || calls != 1 || body != "run capability required\n" {
		t.Fatalf("denied route fell through: status=%d calls=%d body=%q", status, calls, body)
	}
}
