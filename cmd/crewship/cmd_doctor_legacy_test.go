//go:build !clionly

package main

// `crewship doctor` surfaces the legacy-C1-resource status reported by the
// server's /healthz endpoint. The helper is table-tested against a stub HTTP
// server — no crewshipd or Docker involved.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckLegacyResources(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		status     int
		wantStatus string
		wantDetail string // substring
		wantHint   string // substring, "" = don't care
	}{
		{
			name:       "clean passes",
			body:       `{"status":"ok","legacy_resources":"clean"}`,
			status:     http.StatusOK,
			wantStatus: "PASS",
			wantDetail: "no orphaned",
		},
		{
			name:       "present warns with prune hint",
			body:       `{"status":"ok","legacy_resources":"present"}`,
			status:     http.StatusOK,
			wantStatus: "WARN",
			wantDetail: "orphaned pre-C1",
			wantHint:   "prune-legacy",
		},
		{
			name:       "older/non-docker server without the field is informational",
			body:       `{"status":"ok"}`,
			status:     http.StatusOK,
			wantStatus: "INFO",
			wantDetail: "does not report",
		},
		{
			name:       "non-200 healthz is informational, not a duplicate FAIL",
			body:       `oops`,
			status:     http.StatusBadGateway,
			wantStatus: "INFO",
			wantDetail: "502",
		},
		{
			name:       "unknown value is informational",
			body:       `{"status":"ok","legacy_resources":"weird"}`,
			status:     http.StatusOK,
			wantStatus: "INFO",
			wantDetail: "unknown legacy-resource status",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got := checkLegacyResources(context.Background(), srv.URL)
			if got.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (detail: %s)", got.status, tc.wantStatus, got.detail)
			}
			if !strings.Contains(got.detail, tc.wantDetail) {
				t.Fatalf("detail %q does not contain %q", got.detail, tc.wantDetail)
			}
			if tc.wantHint != "" && !strings.Contains(got.hint, tc.wantHint) {
				t.Fatalf("hint %q does not contain %q", got.hint, tc.wantHint)
			}
		})
	}
}

func TestCheckLegacyResources_ServerUnreachable(t *testing.T) {
	got := checkLegacyResources(context.Background(), "http://127.0.0.1:1")
	if got.status != "INFO" {
		t.Fatalf("status = %q, want INFO when server is unreachable", got.status)
	}
}
