package main

// `crewship system openapi` — CLI parity for GET /openapi.json (#1325). The
// spec route exists but had no CLI, so an agent wanting the API contract had to
// hand-roll HTTP.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestIsJSONContentType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/openapi+json", true},
		{" application/json ", true},
		// The failure #1325 fixed: the SPA catch-all answering with index.html.
		{"text/html; charset=utf-8", false},
		{"text/plain", false},
		{"", false},
	} {
		if got := isJSONContentType(tc.in); got != tc.want {
			t.Errorf("isJSONContentType(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSystemOpenAPI(t *testing.T) {
	const spec = `{"openapi":"3.1.0","paths":{"/api/v1/crews":{}}}`

	t.Run("streams the spec verbatim to stdout", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/openapi.json", func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(spec), "application/json"
		})
		covResetFlags(t, systemOpenAPICmd)
		out := covCaptureStdoutCli3(t, func() {
			if err := systemOpenAPICmd.RunE(systemOpenAPICmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		// Byte-for-byte: the spec is the artifact, so no reformatting.
		if strings.TrimSpace(out) != spec {
			t.Errorf("spec not passed through verbatim:\ngot  %q\nwant %q", out, spec)
		}
	})

	t.Run("--out writes to a file instead", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/openapi.json", func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(spec), "application/json"
		})
		dest := filepath.Join(t.TempDir(), "openapi.json")
		covResetFlags(t, systemOpenAPICmd)
		if err := systemOpenAPICmd.Flags().Set("out", dest); err != nil {
			t.Fatalf("set flag: %v", err)
		}
		covCaptureAll(t, func() {
			if err := systemOpenAPICmd.RunE(systemOpenAPICmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read %s: %v", dest, err)
		}
		if string(got) != spec {
			t.Errorf("file = %q, want %q", got, spec)
		}
	})

	t.Run("refuses an HTML body from the SPA catch-all", func(t *testing.T) {
		// A 200 carrying index.html looks like success; writing it into the
		// operator's openapi.json is worse than failing loudly.
		stub := covStub(t)
		stub.OnGet("/openapi.json", func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte("<!doctype html>"), "text/html; charset=utf-8"
		})
		covResetFlags(t, systemOpenAPICmd)
		err := systemOpenAPICmd.RunE(systemOpenAPICmd, nil)
		if err == nil || !strings.Contains(err.Error(), "instead of JSON") {
			t.Fatalf("want an explicit non-JSON error, got %v", err)
		}
	})

	t.Run("works with a stale stored workspace, and sends no workspace_id", func(t *testing.T) {
		// The spec describes the whole instance and is served unauthenticated
		// outside /api/v1 — it never needed a workspace. Before this guard the
		// shared client's per-request slug resolution turned a stale stored
		// workspace into "workspace not found" on a command that doesn't use one.
		stub := covStub(t)
		stub.OnGet("/openapi.json", func(*http.Request, []byte) (int, []byte, string) {
			return 200, []byte(spec), "application/json"
		})
		// Any /workspaces preflight would 404 the stale slug — if resolution
		// still ran, the command would fail here instead of returning the spec.
		stub.OnGet("/api/v1/workspaces", clitest.ErrorResponse(404, "workspace not found"))
		covResetFlags(t, systemOpenAPICmd)

		out := covCaptureStdoutCli3(t, func() {
			if err := systemOpenAPICmd.RunE(systemOpenAPICmd, nil); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if strings.TrimSpace(out) != spec {
			t.Errorf("spec not returned:\n%s", out)
		}
		calls := stub.CallsFor("GET", "/openapi.json")
		if len(calls) != 1 {
			t.Fatalf("want 1 GET, got %d", len(calls))
		}
		if strings.Contains(calls[0].Query, "workspace_id") {
			t.Errorf("a workspace-agnostic route must not carry ?workspace_id=, got %q", calls[0].Query)
		}
	})

	t.Run("api error propagates", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/openapi.json", clitest.ErrorResponse(404, "not found"))
		covResetFlags(t, systemOpenAPICmd)
		err := systemOpenAPICmd.RunE(systemOpenAPICmd, nil)
		if err == nil {
			t.Fatal("expected an error for a 404 spec route")
		}
	})
}
