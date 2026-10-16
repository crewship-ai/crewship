package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

type selectedAgentRoundTrip func(*http.Request) (*http.Response, error)

func (f selectedAgentRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func executeSelectedAgentCLI(t *testing.T, transport http.RoundTripper, stdin string, args ...string) (string, error) {
	t.Helper()
	profileLoads := 0
	parent := &cobra.Command{Use: "crewship", PersistentPreRun: func(*cobra.Command, []string) { profileLoads++ }}
	for _, flag := range []string{"server", "profile", "workspace"} {
		parent.PersistentFlags().String(flag, "", "unavailable to protected adapter")
	}
	parent.PersistentFlags().Bool("server-allow-mismatch", false, "unavailable")
	slide := &cobra.Command{Use: "slide"}
	slide.AddCommand(newSlideAgentCommandWithTransport(transport))
	parent.AddCommand(slide)
	var output bytes.Buffer
	parent.SetOut(&output)
	parent.SetErr(&output)
	parent.SetIn(strings.NewReader(stdin))
	parent.SetArgs(append([]string{"slide", "agent"}, args...))
	err := parent.Execute()
	if profileLoads != 0 {
		t.Fatal("protected command entered human profile/config loader")
	}
	return output.String(), err
}

func TestSlidesAgentCLIRejectsFallbackOverridesAndInvalidIdentity(t *testing.T) {
	calls := 0
	transport := selectedAgentRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not dial") })
	t.Setenv("CREWSHIP_AGENT_TOKEN", "")
	t.Setenv("CREWSHIP_SERVER", "http://untrusted-server.invalid")
	t.Setenv("CREWSHIP_PROFILE", "human-profile-must-not-load")
	if _, err := executeSelectedAgentCLI(t, transport, "", "read", "report", "request"); err == nil {
		t.Fatal("missing run capability fell back to human profile")
	}
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.synthetic")
	for _, args := range [][]string{{"read", "report", "request", "--server", "http://evil.invalid"}, {"read", "report", "request", "--profile", "human"}, {"read", "../report", "request"}, {"read", "report", "request?refresh=1"}} {
		if _, err := executeSelectedAgentCLI(t, transport, "", args...); err == nil {
			t.Fatal("protected override/path accepted", args)
		}
	}
	if calls != 0 {
		t.Fatal("invalid capability reached transport", calls)
	}
}

func TestSlidesAgentCLIRedirectDeniedAndBearerScrubbed(t *testing.T) {
	token := internaltoken.DeriveAgentRunToken("synthetic-run-key", "workspace", "agent", "run")
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	calls := 0
	redirect := selectedAgentRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != selectedAgentOrigin+"/api/v1/pages/report/slides/requests/request" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("fixed origin or original bearer lost")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://evil.invalid/capture"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	output, err := executeSelectedAgentCLI(t, redirect, "", "read", "report", "request")
	if err == nil || calls != 1 || strings.Contains(output, token) || strings.Contains(err.Error(), token) {
		t.Fatal("redirect followed or bearer exposed", calls, err)
	}
	reflectBearer := selectedAgentRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"text":"` + token + `"}`)), Request: r}, nil
	})
	output, err = executeSelectedAgentCLI(t, reflectBearer, "", "read", "report", "request")
	if err != nil || strings.Contains(output, token) || !strings.Contains(output, "[REDACTED]") {
		t.Fatal("reflected bearer reached output", err)
	}
}

func TestSlidesAgentCLIActualSidecarPreservesWireAndStopsOnConflict(t *testing.T) {
	const key = "synthetic-selected-cli-run-key"
	token := internaltoken.DeriveAgentRunToken(key, "ws", "agent", "run")
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	t.Setenv("CREWSHIP_SERVER", "http://ignored-human-profile.invalid")
	t.Setenv("HTTP_PROXY", "http://ignored-proxy.invalid")
	t.Setenv("HTTPS_PROXY", "http://ignored-proxy.invalid")
	body := `{"page_id":"original_page","request_schema_version":1,"request_id":"request","replacement":"Česky\nLF"}`
	reads, replaces := 0, 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("sidecar failed to preserve original bearer or IPC boundary")
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/internal/slides/report/requests/request" {
			reads++
			_, _ = w.Write([]byte(`{"request":{"id":"request"}}`))
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/internal/slides/report/requests/request/replace" {
			t.Error("unexpected protected host route", r.Method, r.URL.Path)
		}
		replaces++
		actual, _ := io.ReadAll(r.Body)
		if string(actual) != body {
			t.Error("rewrote immutable replacement wire", string(actual))
		}
		w.WriteHeader(409)
		_, _ = w.Write([]byte(`{"error":"Original revision changed"}`))
	}))
	defer host.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	proxy := sidecar.NewServer(sidecar.ServerConfig{Addr: address, IPC: &sidecar.IPCConfig{BaseURL: host.URL, Token: "synthetic-crew-ipc", AgentID: "agent", AgentSlug: "agent", CrewID: "crew", WorkspaceID: "ws", RunID: "run", RunChatID: "chat", AgentRunKey: key}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- proxy.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned CLI sidecar did not stop")
		}
	})
	select {
	case <-proxy.Ready():
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("owned CLI sidecar did not start")
	}
	// Trusted test-only TCP mapping retains the production URL/origin and
	// real sidecar handler; no production flag/config can remap this endpoint.
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != "127.0.0.1:9119" {
			return nil, errors.New("unexpected protected origin")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	if _, err = executeSelectedAgentCLI(t, transport, "", "read", "report", "request"); err != nil {
		t.Fatal(err)
	}
	if _, err = executeSelectedAgentCLI(t, transport, body, "replace", "report", "request", "--file", "-"); err == nil || !strings.Contains(err.Error(), "HTTP409") {
		t.Fatal("conflict hidden", err)
	}
	if reads != 1 || replaces != 1 {
		t.Fatal("conflict triggered read/rebase/retry", reads, replaces)
	}
}
