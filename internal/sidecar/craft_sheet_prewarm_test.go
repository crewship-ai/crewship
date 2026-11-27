package sidecar

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// listTools asks the routines MCP for its tools the way the runtime does.
func listCraftSheetTools(t *testing.T, s *Server, token string) (*httptest.ResponseRecorder, time.Duration) {
	t.Helper()
	handler := s.buildHandler(NewProxy(ProxyConfig{CredStore: NewCredStore(), Allowlist: NewDomainAllowlist(nil), Logger: testLogger()}))
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9119/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(w, r)
	return w, time.Since(started)
}

func prewarmHost(t *testing.T, status int, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/v1/internal/craft-native/chat-run" && string(raw) == `{"operation":"prewarm"}` {
			requests.Add(1)
			time.Sleep(delay)
			w.WriteHeader(status)
			return
		}
		w.WriteHeader(503)
	}))
	t.Cleanup(host.Close)
	return host, &requests
}

func waitFor(t *testing.T, n *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for n.Load() < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// The prewarm hint is sent once per run when its Sheet tools are listed, never
// without the startup flag, and a slow or refusing host neither breaks the
// listing nor holds it back.
func TestCraftSheetPrewarmIsSentOncePerRunWhenToolsAreListed(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	s.ipc.Token = "synthetic-sheet-ipc"
	host, requests := prewarmHost(t, 202, 0)
	s.ipc.BaseURL = host.URL
	s.ipc.CraftSheetEnabled = true
	for i := 0; i < 3; i++ {
		w, _ := listCraftSheetTools(t, s, token)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"craft_sheet_read"`) {
			t.Fatalf("listing %d: %d %s", i, w.Code, w.Body)
		}
	}
	waitFor(t, requests, 1)
	time.Sleep(150 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("%d prewarm requests for one run", got)
	}
	// Another run is its own.
	other := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-two")
	listCraftSheetTools(t, s, other)
	waitFor(t, requests, 2)
	if got := requests.Load(); got != 2 {
		t.Fatalf("%d prewarm requests for two runs", got)
	}
}

func TestCraftSheetPrewarmNeedsTheFlagAndARunIdentity(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	s.ipc.Token = "synthetic-sheet-ipc"
	host, requests := prewarmHost(t, 202, 0)
	s.ipc.BaseURL = host.URL
	w, _ := listCraftSheetTools(t, s, token) // flag off
	if w.Code != 200 || strings.Contains(w.Body.String(), "craft_sheet_") {
		t.Fatalf("Sheet tools listed without the flag: %s", w.Body)
	}
	s.ipc.CraftSheetEnabled = true
	listCraftSheetTools(t, s, "agtv2.invalid")
	listCraftSheetTools(t, s, "legacy")
	time.Sleep(200 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("%d prewarm requests without the flag or a valid run", got)
	}
}

func TestCraftSheetPrewarmFailureNeitherBreaksNorDelaysTheListing(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		delay  time.Duration
	}{"refused with 403": {403, 0}, "host error": {500, 0}, "slow host": {202, 1500 * time.Millisecond}} {
		t.Run(name, func(t *testing.T) {
			s, key := newRunIdentityServer(t)
			token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
			s.ipc.Token = "synthetic-sheet-ipc"
			host, requests := prewarmHost(t, tc.status, tc.delay)
			s.ipc.BaseURL = host.URL
			s.ipc.CraftSheetEnabled = true
			w, took := listCraftSheetTools(t, s, token)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"craft_sheet_apply"`) {
				t.Fatalf("listing: %d %s", w.Code, w.Body)
			}
			if took > 700*time.Millisecond {
				t.Fatalf("the listing waited %v for the prewarm", took)
			}
			waitFor(t, requests, 1)
		})
	}
}
