package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Drive the actual CLI against an owned HTTP fixture. The repeated value is
// synthetic, not a credential, callback URL or copied waitpoint capability.
func TestAcceptance_WaitpointDecisionOutput(t *testing.T) {
	const workspace = "c0000000000000000000000001"
	capability := strings.Repeat("a1", 16)
	for _, action := range []string{"approve", "reject"} {
		for _, status := range []int{http.StatusOK, http.StatusNotFound} {
			t.Run(action+"/"+http.StatusText(status), func(t *testing.T) {
				var calls atomic.Int64
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/workspaces/"+workspace+"/pipelines/waitpoints/"+capability+"/approve" {
						t.Error("decision request method or route changed")
					}
					if r.Header.Get("Authorization") != "Bearer synthetic-cli-auth" {
						t.Error("decision authentication changed")
					}
					var body struct {
						Approved bool           `json:"approved"`
						Comment  string         `json:"comment"`
						ActionID string         `json:"action_id"`
						Data     map[string]any `json:"data"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error("decision request was not JSON")
					}
					if body.Approved != (action == "approve") || body.Comment != "reviewed" || body.ActionID != "decision" || body.Data["reason"] != "reviewed" {
						t.Error("decision request fields changed")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(`{"ok":true}`))
					} else {
						_, _ = w.Write([]byte(`{"error":"Waitpoint not found"}`))
					}
				}))
				defer srv.Close()
				config := filepath.Join(t.TempDir(), "cli.yaml")
				if err := os.WriteFile(config, []byte("server: "+srv.URL+"\nworkspace: "+workspace+"\ntoken: synthetic-cli-auth\nformat: table\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				out, err := runWaitpointDeciderCLI(t, config, "routine", "waitpoints", action, capability, "--comment", "reviewed", "--action", "decision", "--input", `{"reason":"reviewed"}`)
				if calls.Load() != 1 {
					t.Error("decision command must make exactly one API call")
				}
				if strings.Contains(out, capability) {
					t.Error("decision output echoed the supplied capability")
				}
				if status == http.StatusOK {
					if err != nil {
						t.Fatalf("successful decision exited with error: %v", err)
					}
					want := "Approved waitpoint.\n"
					if action == "reject" {
						want = "Rejected waitpoint.\n"
					}
					if out != want {
						t.Error("unexpected success confirmation")
					}
				} else {
					if err == nil || exitCodeOf(t, err) != cli.ExitNotFound {
						t.Fatal("missing waitpoint must retain its not-found exit code")
					}
					if !strings.Contains(out, "Waitpoint not found") || strings.Contains(out, "Approved waitpoint") || strings.Contains(out, "Rejected waitpoint") {
						t.Error("failed decision must report the API error without a success confirmation")
					}
				}
			})
		}
	}
}

func TestAcceptance_WaitpointErrorOutput(t *testing.T) {
	capability := strings.Repeat("b2", 16)
	for _, action := range []string{"show", "approve", "reject"} {
		for _, format := range []string{"table", "json"} {
			t.Run(action+"/"+format, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if action == "show" {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`[]`))
						return
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error("fixture could not close the owned connection")
						return
					}
					_ = conn.Close() // No HTTP response: exercise a transport failure.
				}))
				defer srv.Close()
				config := filepath.Join(t.TempDir(), "cli.yaml")
				if err := os.WriteFile(config, []byte("server: "+srv.URL+"\nworkspace: c0000000000000000000000001\ntoken: synthetic-cli-auth\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				out, err := runWaitpointDeciderCLI(t, config, "routine", "waitpoints", action, capability, "--format", format)
				wantCode := cli.ExitConnection
				if action == "show" {
					wantCode = cli.ExitNotFound
				}
				if err == nil || exitCodeOf(t, err) != wantCode {
					t.Fatal("waitpoint error changed its exit classification")
				}
				if strings.Contains(out, capability) {
					t.Error("waitpoint error echoed the supplied capability")
				}
				if format == "json" {
					var envelope cli.ErrorEnvelope
					if json.Unmarshal([]byte(out), &envelope) != nil || envelope.Error.ExitCode != wantCode {
						t.Error("waitpoint error lost its structured exit classification")
					}
				}
			})
		}
	}
}

func TestWaitpointTransportErrorPreservesTypes(t *testing.T) {
	covSaveState(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error("fixture could not close the owned connection")
			return
		}
		_ = conn.Close()
	}))
	defer srv.Close()
	cliCfg = &cli.CLIConfig{Server: srv.URL, Workspace: "c0000000000000000000000001", Token: "synthetic-cli-auth"}
	capability := strings.Repeat("c3", 16)
	err := postWaitpointDecision(capability, true, "", "", nil)
	var connectionErr *cli.ConnectionError
	var requestErr *url.Error
	if !errors.As(err, &connectionErr) || !errors.As(err, &requestErr) || !errors.Is(err, io.EOF) || cli.ExitCodeFor(err) != cli.ExitConnection {
		t.Fatal("decision transport failure lost its error type, underlying cause or exit code")
	}
	if strings.Contains(err.Error(), capability) || strings.Contains(requestErr.URL, capability) {
		t.Error("decision transport error retained the supplied capability")
	}
	if !strings.Contains(requestErr.URL, srv.URL) || requestErr.Op != "Post" {
		t.Error("decision transport error lost non-secret request diagnostics")
	}
}
