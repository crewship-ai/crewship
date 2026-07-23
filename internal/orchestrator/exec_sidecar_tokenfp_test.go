package orchestrator

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestSidecarTokenFP_ParsedFromHealth locks the #1385 orphan-detection probe:
// SidecarTokenFP reads the token_fp field a running sidecar advertises on
// /health, and returns "" (unknown, never orphaned) when the sidecar is
// unreachable or predates the field.
func TestSidecarTokenFP_ParsedFromHealth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		body   string
		route  func(provider.ExecConfig) (*provider.ExecResult, error)
		wantFP string
	}{
		{
			name:   "reports fingerprint",
			body:   `{"status":"ok","network_mode":"free","token_fp":"abc123def456"}`, //gitleaks:allow — fake fixture
			wantFP: "abc123def456",
		},
		{
			name:   "pre-#1385 sidecar omits token_fp",
			body:   `{"status":"ok","network_mode":"free"}`,
			wantFP: "",
		},
		{
			name:   "empty token_fp (crew-less sidecar)",
			body:   `{"status":"ok","network_mode":"free","token_fp":""}`,
			wantFP: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := tc.body
			c := &covContainer{
				route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
					return covResult("health", body), nil
				},
			}
			if got := SidecarTokenFP(context.Background(), c, "ctr1"); got != tc.wantFP {
				t.Errorf("SidecarTokenFP = %q, want %q", got, tc.wantFP)
			}
		})
	}
}

// TestSidecarTokenFP_UnreachableSidecar — a container with no healthy sidecar
// yields "" so the reap path treats it as unknown (never orphaned).
func TestSidecarTokenFP_UnreachableSidecar(t *testing.T) {
	t.Parallel()
	c := &covContainer{
		route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
			return covResult("health", `not-json`), nil
		},
	}
	if got := SidecarTokenFP(context.Background(), c, "ctr1"); got != "" {
		t.Errorf("SidecarTokenFP on unhealthy sidecar = %q, want empty", got)
	}
}

// TestSidecarTokenOrphaned locks the fail-safe classification: only a
// non-empty fingerprint mismatch is an orphan; any unknown fingerprint on
// either side is NEVER orphaned, so a reap can't remove a container it can't
// prove is broken.
func TestSidecarTokenOrphaned(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		reported     string
		expected     string
		wantOrphaned bool
	}{
		{name: "match → healthy", reported: "aaaa", expected: "aaaa", wantOrphaned: false},
		{name: "mismatch → orphaned", reported: "aaaa", expected: "bbbb", wantOrphaned: true},
		{name: "sidecar reports nothing → fail-safe", reported: "", expected: "bbbb", wantOrphaned: false},
		{name: "no expected token → fail-safe", reported: "aaaa", expected: "", wantOrphaned: false},
		{name: "both empty → fail-safe", reported: "", expected: "", wantOrphaned: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SidecarTokenOrphaned(tc.reported, tc.expected); got != tc.wantOrphaned {
				t.Errorf("SidecarTokenOrphaned(%q,%q) = %v, want %v", tc.reported, tc.expected, got, tc.wantOrphaned)
			}
		})
	}
}
