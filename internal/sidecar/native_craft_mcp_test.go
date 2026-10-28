package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestNativeCraftMCPActualRouterExposesOnlyBoundNativeTools(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	proxy := NewProxy(ProxyConfig{CredStore: NewCredStore(), Allowlist: NewDomainAllowlist(nil), Logger: testLogger()})
	handler := s.buildHandler(proxy)
	call := func(credential, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9119/mcp/craft-native", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := call(token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"craft_read"`) || !strings.Contains(w.Body.String(), `"craft_apply"`) {
		t.Fatalf("native tools unavailable: %d %s", w.Code, w.Body)
	}
	for _, credential := range []string{"", "legacy", "agtv2.invalid"} {
		if call(credential, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`).Code != 403 {
			t.Fatal("unbound run obtained native tools")
		}
	}
}

func TestNativeCraftMCPPreservesExactAttemptRequestAndHumanBusyRefusal(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	s.ipc.Token = "synthetic-native-ipc"
	arguments := `{"generation":"7", "base_version":"19","request_id":"original-1","commands":[{"command":"text.insert","params":{"text":"synthetic"}}]}`
	expected := `{"operation":"apply",` + arguments[1:]
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		if r.URL.Path != "/api/v1/internal/craft-native/task" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-native-ipc" {
			t.Error("original run or fixed transport changed")
		}
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != expected {
			t.Errorf("original generation/base/commands changed: %s", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		io.WriteString(w, `{"error":"human holds document","holder":{"kind":"human","name":"Pavel"}}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	call := func(params string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/mcp/craft-native", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+params+`}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleNativeCraftMCP(w, r)
		return w
	}
	for i := 0; i < 2; i++ {
		failed, body := documentCreateMCPResult(t, call(`{"name":"craft_apply","arguments":`+arguments+`}`))
		if !failed || !strings.Contains(string(body), "human holds document") {
			t.Fatal("human busy refusal lost or claimed success")
		}
	}
	if calls != 2 {
		t.Fatal("sidecar retried operation")
	}
	for _, bad := range []string{
		`{"name":"craft_acquire","arguments":{"page_id":"foreign"}}`,
		`{"name":"craft_apply","arguments":{"generation":"07","base_version":"19","request_id":"x","commands":[{}]}}`,
		`{"name":"craft_renew","arguments":{"generation":7}}`,
		`{"name":"craft_read","arguments":{"run_id":"foreign"}}`,
		`{"name":"craft_release","arguments":{"generation":"7","generation":"8"}}`,
		`{"name":"craft_apply","arguments":{"generation":"7","base_version":"19","request_id":"x","commands":[{"params":{"text":"a","text":"b"}}]}}`,
		`{"name":"craft_apply","arguments":{"generation":"7","base_version":"19","request_id":"x","commands":[]}}`,
	} {
		w := call(bad)
		if w.Code == http.StatusBadRequest {
			continue
		}
		failed, _ := documentCreateMCPResult(t, w)
		if !failed {
			t.Fatal("ambiguous/foreign native operation accepted")
		}
	}
	if calls != 2 {
		t.Fatal("invalid native request reached host")
	}
}

func TestNativeCraftHostAllowsBoundedOriginalEngineConversion(t *testing.T) {
	original := ipcClient
	t.Cleanup(func() { ipcClient = original })
	var budget time.Duration
	ipcClient = &http.Client{Timeout: 30 * time.Second, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("unbounded native engine transport")
		}
		budget = time.Until(deadline)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"phase":"committed"}`))}, nil
	})}
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://synthetic-host", Token: "synthetic", WorkspaceID: "ws"})
	_, err := s.sendBoundedHostRequest(httptest.NewRequest("POST", "/", nil), "POST", "/api/v1/internal/craft-native/task", []byte(`{}`), 1024, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if budget < 60*time.Second || budget > 65*time.Second {
		t.Fatalf("original engine validation plus apply budget: %v", budget)
	}
	_, err = s.sendBoundedHostRequest(httptest.NewRequest("POST", "/", nil), "POST", "/api/v1/internal/mooring/task", []byte(`{}`), 1024, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if budget > 10*time.Second {
		t.Fatal("native budget broadened unrelated operations")
	}
}
