package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/cli"
)

func keeperAgentFixtureCapability() string {
	return internaltoken.DeriveAgentRunToken(strings.Repeat("0", 64), "fixture-workspace", "fixture-agent", "fixture-run")
}

func TestKeeperRuntimeCommandDiscovery(t *testing.T) {
	for _, name := range []string{"request", "execute", "request-status"} {
		cmd, remaining, err := keeperCmd.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name || len(remaining) != 0 {
			t.Errorf("native agent Keeper command %q unavailable", name)
		}
	}
}

func executeKeeperAgentFixture(t *testing.T, operation, input string, args ...string) (string, error) {
	t.Helper()
	cmd := newKeeperAgentCommand(operation)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestKeeperAgentNativeRequestUsesRunCapabilityAndSafeShape(t *testing.T) {
	const token = "agtv2.synthetic-original-run"
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/keeper/request" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong Keeper route or run capability")
		}
		var body map[string]string
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["credential_name"] != "NATIVE_SECRET" || body["intent"] != "fixture authentication" || body["approval_request_id"] != "kr_approved" {
			t.Error("native request lost its explicit credential/intent/approval")
		}
		if len(body) != 3 || body["agent_id"] != "" || body["run_id"] != "" {
			t.Error("request invented identity or added unexpected material")
		}
		io.WriteString(w, `{"request_id":"kr_fixture","decision":"ALLOW","risk_score":1,"reason":"`+token+`","credential_value":"opaque-secret-must-not-be-printed"}`)
	}))
	defer s.Close()
	t.Setenv("CREWSHIP_SIDECAR_URL", s.URL)
	t.Setenv("CREWSHIP_SERVER", "https://human-profile.invalid")
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	out, err := executeKeeperAgentFixture(t, "request", "", "NATIVE_SECRET", "--intent", "fixture authentication", "--approval-request-id", "kr_approved")
	if err != nil || calls != 1 || proxyCalls.Load() != 0 {
		t.Fatalf("native request failed or used proxy: err=%v calls=%d proxy=%d", err, calls, proxyCalls.Load())
	}
	if strings.Contains(out, token) || strings.Contains(out, "opaque-secret") || !strings.Contains(out, `"decision":"ALLOW"`) {
		t.Fatal("response disclosed unknown credential fields/run capability or lost the decision")
	}
}

func TestKeeperAgentDecisionAndToolExitAreNotAuthenticationSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, operation, body string
		want                  int
	}{
		{"allowed request", "request", `{"request_id":"kr_fixture","decision":"ALLOW"}`, 0},
		{"denied", "execute", `{"request_id":"kr_fixture","decision":"DENY"}`, cli.ExitAuth},
		{"escalated", "execute", `{"request_id":"kr_fixture","decision":"ESCALATE"}`, cli.ExitConflict},
		{"pending", "request", `{"request_id":"kr_fixture","decision":"PENDING"}`, cli.ExitConflict},
		{"tool failure", "execute", `{"request_id":"kr_fixture","decision":"ALLOW","exit_code":73}`, 73},
		{"zero omitted by API", "execute", `{"request_id":"kr_fixture","decision":"ALLOW"}`, 0},
		{"unknown decision", "request", `{"request_id":"kr_fixture","decision":"UNRECOGNIZED"}`, cli.ExitServer},
		{"missing identity", "request", `{"decision":"ALLOW"}`, cli.ExitServer},
		{"invalid JSON", "request", `{"request_id":`, cli.ExitServer},
		{"status pending", "request-status", `{"id":"kr_fixture","decision":null}`, cli.ExitConflict},
		{"wrong status identity", "request-status", `{"id":"kr_other","decision":"ALLOW"}`, cli.ExitServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_AGENT_TOKEN", keeperAgentFixtureCapability())
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.operation == "execute" {
					var body map[string]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["command"] != "glab api user" || body["credential_id"] != "kr_fixture" || body["credential_name"] != "" {
						t.Error("execute did not read the native command/ID from stdin and flags")
					}
				}
				if tc.operation == "request-status" && (r.Method != "GET" || r.URL.Path != "/keeper/request/kr_fixture") {
					t.Error("wrong native status route")
				}
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			t.Setenv("CREWSHIP_SIDECAR_URL", s.URL)
			args := []string{"kr_fixture"}
			if tc.operation != "request-status" {
				args = append(args, "--intent", "fixture authentication")
			}
			if tc.operation == "execute" {
				args = append(args, "--command-stdin", "--id")
			}
			_, err := executeKeeperAgentFixture(t, tc.operation, "glab api user\n", args...)
			if got := cli.ExitCodeFor(err); got != tc.want {
				t.Fatalf("exit=%d want=%d err=%v", got, tc.want, err)
			}
		})
	}
}

func TestKeeperAgentRejectsInvalidInputWithoutNetworkOrHumanFallback(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer s.Close()
	for _, tc := range []struct {
		name, token, endpoint, input string
		args                         []string
	}{
		{"no token", "", s.URL, "", []string{"SECRET", "--intent", "fixture"}},
		{"legacy token", "legacy-token", s.URL, "", []string{"SECRET", "--intent", "fixture"}},
		{"token newline", "agtv2.bad\nvalue", s.URL, "", []string{"SECRET", "--intent", "fixture"}},
		{"no intent", "agtv2.fixture", s.URL, "", []string{"SECRET"}},
		{"oversized intent", "agtv2.fixture", s.URL, "", []string{"SECRET", "--intent", strings.Repeat("x", 4097)}},
		{"remote", "agtv2.fixture", "http://example.invalid:9119", "", []string{"SECRET", "--intent", "fixture"}},
		{"TLS", "agtv2.fixture", "https://127.0.0.1:9119", "", []string{"SECRET", "--intent", "fixture"}},
		{"path", "agtv2.fixture", s.URL + "/escape", "", []string{"SECRET", "--intent", "fixture"}},
		{"userinfo", "agtv2.fixture", "http://user@127.0.0.1:9119", "", []string{"SECRET", "--intent", "fixture"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_AGENT_TOKEN", tc.token)
			t.Setenv("CREWSHIP_SIDECAR_URL", tc.endpoint)
			out, err := executeKeeperAgentFixture(t, "request", tc.input, tc.args...)
			if err == nil || out != "" {
				t.Fatal("invalid Keeper input produced a successful request")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("invalid native request reached a sidecar or human server")
	}
}

func TestKeeperAgentRefusesHumanProfileOverrides(t *testing.T) {
	root := &cobra.Command{Use: "fixture", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("server", "", "fixture")
	root.AddCommand(newKeeperAgentCommand("request"))
	root.SetArgs([]string{"--server", "https://human.invalid", "request", "SECRET", "--intent", "fixture"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "overrides") {
		t.Fatal("agent Keeper accepted a human server override")
	}
}

func TestKeeperAgentCommandStdinBoundsAndTextAreEnforced(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", keeperAgentFixtureCapability())
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"request_id":"kr_fixture","decision":"ALLOW"}`)
	}))
	defer s.Close()
	t.Setenv("CREWSHIP_SIDECAR_URL", s.URL)
	for _, input := range []string{"", " \n", strings.Repeat("x", 4097), "glab\x00api", "glab\xffapi"} {
		if out, err := executeKeeperAgentFixture(t, "execute", input, "SECRET", "--intent", "fixture", "--command-stdin"); err == nil || out != "" {
			t.Fatal("invalid command stdin reached execution")
		}
	}
	if _, err := executeKeeperAgentFixture(t, "execute", "glab api user", "SECRET", "--intent", "fixture"); err == nil {
		t.Fatal("execute accepted a command without the explicit stdin flag")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid stdin reached the sidecar")
	}
	if _, err := executeKeeperAgentFixture(t, "execute", strings.Repeat("x", 4096), "SECRET", "--intent", "fixture", "--command-stdin"); err != nil || calls.Load() != 1 {
		t.Fatal("valid boundary-sized command was refused")
	}
}

func TestKeeperAgentAmbiguousExecutionReplyNeverClaimsRejectionOrRetries(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{502, `{"error":"opaque-secret"}`},
		{200, `{"request_id":`},
		{200, `{"decision":"ALLOW"}`},
	} {
		t.Run(tc.body, func(t *testing.T) {
			t.Setenv("CREWSHIP_AGENT_TOKEN", keeperAgentFixtureCapability())
			var executions atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/keeper/execute" {
					t.Error("wrong execution route")
				}
				executions.Add(1) // command ran; its reply is then lost/damaged
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			t.Setenv("CREWSHIP_SIDECAR_URL", s.URL)
			out, err := executeKeeperAgentFixture(t, "execute", "glab api user", "SECRET", "--intent", "fixture", "--command-stdin")
			if err == nil || !strings.Contains(err.Error(), "outcome unavailable") || !strings.Contains(err.Error(), "inspect") || strings.Contains(err.Error(), "rejected") || strings.Contains(err.Error(), "opaque-secret") || out != "" || executions.Load() != 1 {
				t.Fatalf("ambiguous result misrepresented or retried: err=%v executions=%d", err, executions.Load())
			}
		})
	}
}

func TestKeeperAgentRedirectErrorsAndCancellationAreBounded(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", keeperAgentFixtureCapability())
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer other.Close()
	for _, status := range []int{302, 403, 500} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", other.URL)
			w.WriteHeader(status)
			io.WriteString(w, "opaque-credential-error-must-not-appear")
		}))
		t.Setenv("CREWSHIP_SIDECAR_URL", s.URL)
		out, err := executeKeeperAgentFixture(t, "request", "", "SECRET", "--intent", "fixture")
		s.Close()
		if err == nil || out != "" || strings.Contains(err.Error(), "opaque-credential") {
			t.Fatal("redirect/error body became success or disclosed server data")
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("Keeper bearer followed a redirect")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := newKeeperAgentCommand("request")
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"SECRET", "--intent", "fixture"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	start := time.Now()
	err := cmd.Execute()
	if err == nil || cli.ExitCodeFor(err) != cli.ExitGeneric || !strings.Contains(err.Error(), "outcome unavailable") || time.Since(start) > time.Second {
		t.Fatal("cancelled Keeper command did not stop with an unknown outcome")
	}
}

// #5548: 400/404/409 from execute are refused before any decision, approval
// consumption or execution, so the CLI reports a definite refusal with fixed
// text and the matching exit code. 5xx and other statuses stay ambiguous.
func TestKeeperAgentExecuteReportsKnownPreExecutionRefusals(t *testing.T) {
	for _, tc := range []struct {
		status    int
		wantExit  int
		want      []string
		ambiguous bool
	}{
		{400, cli.ExitValidation, []string{"refused the command before execution", "no shell operators", "HTTP 400"}, false},
		{404, cli.ExitNotFound, []string{"nothing executed", "grant expired", "HTTP 404"}, false},
		{409, cli.ExitConflict, []string{"nothing executed by this call", "approval already used", "HTTP 409"}, false},
		{403, cli.ExitAuth, nil, true},
		{500, cli.ExitGeneric, nil, true},
		{502, cli.ExitGeneric, nil, true},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			t.Setenv("CREWSHIP_AGENT_TOKEN", keeperAgentFixtureCapability())
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				io.WriteString(w, `{"error":"opaque-secret"}`)
			}))
			defer s.Close()
			t.Setenv("CREWSHIP_SIDECAR_URL", s.URL)
			out, err := executeKeeperAgentFixture(t, "execute", "glab api user", "SECRET", "--intent", "fixture", "--command-stdin")
			if err == nil || out != "" || strings.Contains(err.Error(), "opaque-secret") {
				t.Fatalf("want error without output or server body, got out=%q err=%v", out, err)
			}
			msg := err.Error()
			if tc.ambiguous {
				if !strings.Contains(msg, "outcome unavailable") {
					t.Fatalf("ambiguous status must stay outcome unavailable: %v", err)
				}
				return
			}
			if strings.Contains(msg, "outcome unavailable") || cli.ExitCodeFor(err) != tc.wantExit {
				t.Fatalf("known refusal misreported: exit=%d err=%v", cli.ExitCodeFor(err), err)
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q missing %q", msg, w)
				}
			}
		})
	}
}
