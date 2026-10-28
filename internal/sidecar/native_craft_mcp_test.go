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

// Protocol-only synthetic HTTP fixture. This proves request normalization and
// refusal boundaries, not an authenticated provider run or a native receipt.
func TestNativeCraftMCPOptionalArgumentsHTTPContract(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	s.ipc.Token = "fixture-native-ipc"
	forwarded := make(chan string, 32)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/craft-native/task" ||
			r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "fixture-native-ipc" {
			t.Error("fixed route or original synthetic authentication changed")
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		forwarded <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"fixture_only":true}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	proxy := NewProxy(ProxyConfig{CredStore: NewCredStore(), Allowlist: NewDomainAllowlist(nil), Logger: testLogger()})
	handler := s.buildHandler(proxy)
	for _, tc := range []struct {
		name, params, packet string
	}{
		{"acquire-omitted", `{"name":"craft_acquire"}`, `{"operation":"acquire"}`},
		{"acquire-explicit-empty-calibration", `{"name":"craft_acquire","arguments":{}}`, `{"operation":"acquire"}`},
		{"read-omitted", `{"name":"craft_read"}`, `{"operation":"read"}`},
		{"read-explicit-empty-calibration", `{"name":"craft_read","arguments":{}}`, `{"operation":"read"}`},
		{"acquire-meta-is-not-forwarded", `{"name":"craft_acquire","_meta":{"progressToken":"fixture"}}`, `{"operation":"acquire"}`},
		{"acquire-null", `{"name":"craft_acquire","arguments":null}`, ""},
		{"read-null", `{"name":"craft_read","arguments":null}`, ""},
		{"acquire-array", `{"name":"craft_acquire","arguments":[]}`, ""},
		{"acquire-string", `{"name":"craft_acquire","arguments":"{}"}`, ""},
		{"acquire-number", `{"name":"craft_acquire","arguments":1}`, ""},
		{"acquire-selector", `{"name":"craft_acquire","arguments":{"page_id":"foreign"}}`, ""},
		{"read-selector", `{"name":"craft_read","arguments":{"run_id":"foreign"}}`, ""},
		{"unknown-name", `{"name":"craft_unknown"}`, ""},
		{"unprefixed-name", `{"name":"acquire"}`, ""},
		{"renew-omitted-required-generation", `{"name":"craft_renew"}`, ""},
		{"release-omitted-required-generation", `{"name":"craft_release"}`, ""},
		{"apply-omitted-required-fields", `{"name":"craft_apply"}`, ""},
		{"renew-empty-required-generation", `{"name":"craft_renew","arguments":{}}`, ""},
		{"release-empty-required-generation", `{"name":"craft_release","arguments":{}}`, ""},
		{"apply-empty-required-fields", `{"name":"craft_apply","arguments":{}}`, ""},
		{"null-meta", `{"name":"craft_acquire","_meta":null}`, ""},
		{"outer-selector", `{"name":"craft_acquire","page_id":"foreign"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9119/mcp/craft-native", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+tc.params+`}`))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			failed, _ := documentCreateMCPResult(t, w)
			if tc.packet != "" {
				if failed {
					t.Errorf("valid optional-arguments call was refused: %s", w.Body)
				}
				select {
				case got := <-forwarded:
					if got != tc.packet {
						t.Errorf("host packet=%s want=%s", got, tc.packet)
					}
				default:
					t.Error("valid optional-arguments call did not reach synthetic host")
				}
			} else {
				if !failed {
					t.Error("invalid or incomplete operation was accepted")
				}
				select {
				case got := <-forwarded:
					t.Errorf("refused operation reached host: %s", got)
				default:
				}
			}
		})
	}
}
