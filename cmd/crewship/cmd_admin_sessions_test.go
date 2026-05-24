//go:build !clionly

package main

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyAdminSessionRow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	rfc := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	cases := []struct {
		name      string
		revokedAt string
		expiresAt string
		want      string
	}{
		{
			name:      "revoked wins over fresh expiry",
			revokedAt: rfc(-1 * time.Hour),
			expiresAt: rfc(7 * 24 * time.Hour),
			want:      "revoked",
		},
		{
			name:      "revoked wins over past expiry",
			revokedAt: rfc(-1 * time.Hour),
			expiresAt: rfc(-2 * time.Hour),
			want:      "revoked",
		},
		{
			name:      "not revoked + future expiry → active",
			revokedAt: "",
			expiresAt: rfc(30 * time.Minute),
			want:      "active",
		},
		{
			name:      "not revoked + far future → active",
			revokedAt: "",
			expiresAt: rfc(7 * 24 * time.Hour),
			want:      "active",
		},
		{
			name:      "not revoked + past expiry → expired",
			revokedAt: "",
			expiresAt: rfc(-1 * time.Minute),
			want:      "expired",
		},
		{
			name:      "expiry exactly = now → expired (boundary: strict After)",
			revokedAt: "",
			expiresAt: rfc(0),
			want:      "expired",
		},
		{
			name:      "SQLite space-separator future → active",
			revokedAt: "",
			expiresAt: now.Add(1 * time.Hour).UTC().Format("2006-01-02 15:04:05"),
			want:      "active",
		},
		{
			name:      "SQLite space-separator past → expired",
			revokedAt: "",
			expiresAt: now.Add(-1 * time.Hour).UTC().Format("2006-01-02 15:04:05"),
			want:      "expired",
		},
		{
			name:      "malformed expiry → active (server bug fallback)",
			revokedAt: "",
			expiresAt: "not-a-timestamp",
			want:      "active",
		},
		{
			name:      "whitespace-only revoked → not revoked (active)",
			revokedAt: "   ",
			expiresAt: rfc(1 * time.Hour),
			want:      "active",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyAdminSessionRow(tc.revokedAt, tc.expiresAt, now)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShortAdminTime(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", "-"},
		{"   ", "-"},
		{"2026-05-24T12:34:56Z", "2026-05-24 12:34"},
		{"2026-05-24 12:34:56", "2026-05-24 12:34"},
		{"not-a-timestamp", "not-a-timestamp"}, // pass through unchanged
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := shortAdminTime(tc.in); got != tc.want {
				t.Errorf("shortAdminTime(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAdminSessionsListCmd_Wiring guards flag + subcommand
// registration. NOT parallel: mutates the shared cobra cmd state
// like the iter 23-fixed sibling tests.
func TestAdminSessionsListCmd_Wiring(t *testing.T) {
	for _, name := range []string{"email", "active-only", "limit"} {
		if f := adminSessionsListCmd.Flags().Lookup(name); f == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
	// Subcommand must be registered under adminSessionsCmd, which
	// must be registered under adminCmd — otherwise the path
	// `crewship admin sessions list` won't resolve.
	found := false
	for _, c := range adminSessionsCmd.Commands() {
		if c == adminSessionsListCmd {
			found = true
			break
		}
	}
	if !found {
		t.Error("adminSessionsListCmd not registered under adminSessionsCmd")
	}
	if !strings.Contains(adminSessionsCmd.Use, "sessions") {
		t.Errorf("adminSessionsCmd.Use = %q, want substring \"sessions\"", adminSessionsCmd.Use)
	}
}
