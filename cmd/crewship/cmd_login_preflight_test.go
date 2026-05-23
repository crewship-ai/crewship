package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestPreflightServerURL exercises the login pre-flight transport
// audit. The matrix:
//
//	URL shape                      → result
//	─────────────────────────────────────────────────────────
//	empty / whitespace             → error (block login)
//	malformed                      → error (block login)
//	missing host (http://:8080)    → error (block login)
//	unsupported scheme (ftp://)    → error (block login)
//	https://*                      → nil, no warning
//	http://loopback                → nil, no warning
//	http://non-loopback            → nil, warning printed
//
// The "warning printed" case is what the new code is for; everything
// else is invariant-pinning so a future refactor doesn't quietly
// weaken any branch.
//
// The implementation sleeps 1 s on the warning path so the user sees
// the message before subsequent prompts overwrite the terminal. We
// don't assert the sleep here (would slow the suite by 1 s × N cases);
// the constant lives at the call site and is reviewable.
func TestPreflightServerURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		server      string
		wantErr     string // substring; empty means "no error"
		wantStderr  string // substring; empty means "no output"
		noOutputOK  bool   // true → stderr MUST be empty
	}{
		{
			name:       "empty → error",
			server:     "",
			wantErr:    "--server is empty",
			noOutputOK: true,
		},
		{
			name:       "whitespace-only → error",
			server:     "   ",
			wantErr:    "--server is empty",
			noOutputOK: true,
		},
		{
			name:       "missing host → error",
			server:     "http://:8080",
			wantErr:    "missing a host",
			noOutputOK: true,
		},
		{
			name:       "https missing host → error",
			server:     "https://:443",
			wantErr:    "missing a host",
			noOutputOK: true,
		},
		{
			name:       "unsupported scheme → error",
			server:     "ftp://files.example.com",
			wantErr:    "scheme \"ftp\" is unsupported",
			noOutputOK: true,
		},
		{
			name:       "https public → silent pass",
			server:     "https://crewship.example.com",
			noOutputOK: true,
		},
		{
			name:       "https with port → silent pass",
			server:     "https://crewship.example.com:8443",
			noOutputOK: true,
		},
		{
			name:       "http localhost → silent pass (dev workflow)",
			server:     "http://localhost:8080",
			noOutputOK: true,
		},
		{
			name:       "http 127.0.0.1 → silent pass",
			server:     "http://127.0.0.1:8080",
			noOutputOK: true,
		},
		{
			name:       "http ::1 → silent pass",
			server:     "http://[::1]:8080",
			noOutputOK: true,
		},
		{
			name:       "http LAN IP → warning",
			server:     "http://192.168.1.201:8080",
			wantStderr: "plaintext HTTP",
		},
		{
			name:       "http public host → warning",
			server:     "http://crewship.example.com",
			wantStderr: "plaintext HTTP",
		},
		{
			name:       "HTTP case-insensitive → warning still fires on non-loopback",
			server:     "HTTP://crewship.example.com",
			wantStderr: "plaintext HTTP",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Skip the sleep-bearing warning cases under the race
			// detector? No — the sleep is unconditional on warning,
			// 1 second total per warning test. Three warning cases =
			// 3 seconds added to the suite. Acceptable; the warning
			// branch needs the visible delay.
			buf := &bytes.Buffer{}
			err := preflightServerURL(buf, tc.server)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (stderr=%q)", tc.wantErr, buf.String())
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want substring %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v (stderr=%q)", err, buf.String())
			}

			out := buf.String()
			if tc.noOutputOK {
				if out != "" {
					t.Errorf("expected no stderr output, got %q", out)
				}
				return
			}
			if tc.wantStderr != "" && !strings.Contains(out, tc.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", out, tc.wantStderr)
			}
		})
	}
}

func TestLoginIsLoopback(t *testing.T) {
	t.Parallel()
	// Lightweight mirror of the doctor isLoopbackHost matrix —
	// keeps the two implementations from drifting silently.
	cases := []struct {
		host string
		want bool
	}{
		{"", false}, // empty must NOT be loopback
		{"localhost", true},
		{"LocalHost", true},
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"::1", true},
		{"crewship.example.com", false},
		{"192.168.1.1", false},
		{"::ffff:127.0.0.1", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.host, func(t *testing.T) {
			t.Parallel()
			if got := loginIsLoopback(tc.host); got != tc.want {
				t.Errorf("loginIsLoopback(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}
