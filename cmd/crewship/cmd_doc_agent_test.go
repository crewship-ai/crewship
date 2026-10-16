package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func executeDocAgentCLI(t *testing.T, transport http.RoundTripper, stdin string, args ...string) (string, error) {
	t.Helper()
	loads := 0
	parent := &cobra.Command{Use: "crewship", PersistentPreRun: func(*cobra.Command, []string) { loads++ }}
	for _, flag := range []string{"server", "profile", "workspace"} {
		parent.PersistentFlags().String(flag, "", "protected")
	}
	parent.PersistentFlags().Bool("server-allow-mismatch", false, "protected")
	oldFormat := flagFormat
	t.Cleanup(func() { flagFormat = oldFormat })
	parent.PersistentFlags().StringVarP(&flagFormat, "format", "f", "json", "presentation")
	doc := &cobra.Command{Use: "doc"}
	doc.AddCommand(newDocAgentCommandWithTransport(transport))
	parent.AddCommand(doc)
	var output bytes.Buffer
	parent.SetOut(&output)
	parent.SetErr(&output)
	parent.SetIn(strings.NewReader(stdin))
	parent.SetArgs(append([]string{"doc", "agent"}, args...))
	err := parent.Execute()
	if loads != 0 {
		t.Fatal("protected adapter entered human profile loader")
	}
	return output.String(), err
}

func TestDocAgentCLIOnlyOperationsAndFixedProtectedTransport(t *testing.T) {
	token := "agtv2.synthetic-original-run"
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	t.Setenv("CREWSHIP_SERVER", "http://evil.invalid")
	calls := 0
	transport := selectedAgentRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != selectedAgentOrigin+"/api/v1/pages/report/document/agent-apply" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("protected origin or original bearer lost")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"operations":[{"op":"delete","id":"selected"}]}` {
			t.Fatalf("model envelope escaped adapter: %s", body)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"rev":8,"duplicate":true}`)), Request: r}, nil
	})
	output, err := executeDocAgentCLI(t, transport, `[{"op":"delete","id":"selected"}]`, "apply", "report", "--operations-file", "-")
	if err != nil || !strings.Contains(output, "duplicate") || calls != 1 {
		t.Fatal(output, err, calls)
	}
	for _, body := range []string{`{"ifRev":8,"operations":[]}`, `[{"op":"delete","id":"selected","if_hash":"forged"}]`, `[{"op":"delete","id":"selected","page_id":"foreign"}]`, `[]`, `[{"op":"move_before","id":"same","before_id":"same"}]`} {
		if _, err := executeDocAgentCLI(t, transport, body, "apply", "report", "--operations-file", "-"); err == nil {
			t.Fatal("model-supplied identity or invalid operations accepted", body)
		}
	}
	if calls != 1 {
		t.Fatal("invalid model input reached transport")
	}
}

func TestDocAgentCLIRejectsFallbackOverridesRedirectAndSecretEcho(t *testing.T) {
	calls := 0
	transport := selectedAgentRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not dial") })
	t.Setenv("CREWSHIP_AGENT_TOKEN", "")
	if _, err := executeDocAgentCLI(t, transport, "", "read", "report"); err == nil {
		t.Fatal("missing capability accepted")
	}
	token := "agtv2.synthetic-original-run"
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	for _, args := range [][]string{{"read", "report", "--server", "http://evil.invalid"}, {"read", "report", "--profile", "human"}, {"read", "report", "--workspace", "other"}, {"read", "../report"}, {"checkpoint", "report", "--operations-file", "-"}} {
		if _, err := executeDocAgentCLI(t, transport, "", args...); err == nil {
			t.Fatal("protected override accepted", args)
		}
	}
	if calls != 0 {
		t.Fatal("invalid capability reached transport")
	}
	redirect := selectedAgentRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://evil.invalid"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	})
	if _, err := executeDocAgentCLI(t, redirect, "", "read", "report"); err == nil || calls != 1 {
		t.Fatal("redirect followed", err, calls)
	}
	reflect := selectedAgentRoundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"` + token + `"}`)), Request: r}, nil
	})
	output, err := executeDocAgentCLI(t, reflect, "", "read", "report")
	if err == nil || strings.Contains(output, token) || strings.Contains(err.Error(), token) {
		t.Fatal("bearer echoed", output, err)
	}
}

func TestDocAgentCLIPreservesBoundedLiteralOperationsBytes(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", internaltoken.DeriveAgentRunToken(strings.Repeat("x", 48), "workspace", "agent", "run"))
	const prefix = `[{"op":"insert_after","after_id":"selected","node":{"type":"paragraph","content":[{"type":"text","text":"`
	const suffix = `"}]}}]`
	for _, filler := range []string{"<", "&", "\u2028"} {
		size := docAgentIntentBytes - len(prefix) - len(suffix)
		operations := prefix + strings.Repeat(filler, size/len(filler)) + strings.Repeat("x", size%len(filler)) + suffix
		transport := selectedAgentRoundTrip(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"operations":`+operations+`}` {
				t.Fatal("CLI inflated/changed valid bounded literal JSON")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"rev":8}`)), Request: r}, nil
		})
		if _, err := executeDocAgentCLI(t, transport, operations, "apply", "report", "--operations-file", "-"); err != nil {
			t.Fatal(err)
		}
	}
}
