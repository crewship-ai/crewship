package main

import (
	"strings"
	"testing"
)

// TestAllSessionInvalid pins down the predicate: every entry in the
// failure list matches the session-expired auth shape. The caller
// composes this with "len(errs) == totalFetchCount" to decide whether
// the *whole* dashboard run is session-expired and should bail
// instead of rendering an empty view (gh#555).
//
// Partial failure (some success, some 401) stays a soft `[partial]`
// warning — that decision lives in the caller. This predicate just
// answers "if I were to bail, are these failures uniformly auth?"
func TestAllSessionInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		errs []string
		want bool
	}{
		{
			name: "empty list — nothing to be uniform about",
			errs: nil,
			want: false,
		},
		{
			name: "single 401 session_invalid — list is uniform",
			errs: []string{"approvals: API error (401): session_invalid"},
			want: true,
		},
		{
			name: "all three 401 — the gh#555 case",
			errs: []string{
				"runs: API error (401): session_invalid",
				"missions: API error (401): session_invalid",
				"approvals: API error (401): session_invalid",
			},
			want: true,
		},
		{
			name: "mix of 401 and 500 — not uniform",
			errs: []string{
				"runs: API error (401): session_invalid",
				"missions: API error (500): internal error",
			},
			want: false,
		},
		{
			name: "all 401 but different reason — only session_invalid counts",
			errs: []string{
				"runs: API error (401): no_credentials",
				"missions: API error (401): no_credentials",
			},
			want: false,
		},
		{
			name: "labels vary, shape uniform",
			errs: []string{
				"a: API error (401): session_invalid",
				"b: API error (401): session_invalid",
			},
			want: true,
		},
		{
			name: "wrapped error string still detected — contains substring",
			errs: []string{
				"runs: fetch failed: API error (401): session_invalid",
				"missions: fetch failed: API error (401): session_invalid",
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := allSessionInvalid(tc.errs); got != tc.want {
				t.Errorf("allSessionInvalid(%v) = %v, want %v", tc.errs, got, tc.want)
			}
		})
	}
}

// TestErrSessionExpired confirms the message users see when re-login
// is needed — must mention `crewship login` so scripts and humans
// have a clear remediation path, not just "auth failed".
func TestErrSessionExpired(t *testing.T) {
	t.Parallel()
	err := errSessionExpired()
	if err == nil {
		t.Fatal("errSessionExpired must return a non-nil error")
	}
	msg := err.Error()
	if !strings.Contains(strings.ToLower(msg), "session") {
		t.Errorf("error should mention session, got: %q", msg)
	}
	if !strings.Contains(msg, "crewship login") {
		t.Errorf("error should suggest `crewship login`, got: %q", msg)
	}
}
