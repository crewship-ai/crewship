package main

import (
	"testing"
	"time"
)

func TestClassifyTokenStatus(t *testing.T) {
	t.Parallel()

	// Fixed "now" so the table cases are reproducible. All test
	// timestamps are anchored relative to this. UTC mirrors what
	// the server emits via RFC3339.
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	rfc := func(d time.Duration) string {
		return now.Add(-d).Format(time.RFC3339)
	}
	rfcPtr := func(d time.Duration) *string {
		s := rfc(d)
		return &s
	}

	cases := []struct {
		name          string
		createdAt     string
		lastUsedAt    *string
		revokedAt     *string
		warnStaleDays int
		want          string
	}{
		{
			name:          "revoked wins even when fresh",
			createdAt:     rfc(1 * time.Hour),
			lastUsedAt:    rfcPtr(30 * time.Minute),
			revokedAt:     rfcPtr(5 * time.Minute),
			warnStaleDays: 90,
			want:          "revoked",
		},
		{
			name:          "revoked wins even when stale",
			createdAt:     rfc(365 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(200 * 24 * time.Hour),
			revokedAt:     rfcPtr(180 * 24 * time.Hour),
			warnStaleDays: 90,
			want:          "revoked",
		},
		{
			name:          "fresh active token",
			createdAt:     rfc(2 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(1 * time.Hour),
			warnStaleDays: 90,
			want:          "active",
		},
		{
			name:          "old created but recently used = active",
			createdAt:     rfc(365 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(2 * time.Hour),
			warnStaleDays: 90,
			want:          "active",
		},
		{
			name:          "last used over threshold = stale",
			createdAt:     rfc(365 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(100 * 24 * time.Hour),
			warnStaleDays: 90,
			want:          "stale",
		},
		{
			name:          "exactly at threshold = active (strictly greater is stale)",
			createdAt:     rfc(365 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(90 * 24 * time.Hour),
			warnStaleDays: 90,
			want:          "active",
		},
		{
			name:          "never used, young = active",
			createdAt:     rfc(2 * 24 * time.Hour),
			lastUsedAt:    nil,
			warnStaleDays: 90,
			want:          "active",
		},
		{
			name:          "never used, old = unused",
			createdAt:     rfc(120 * 24 * time.Hour),
			lastUsedAt:    nil,
			warnStaleDays: 90,
			want:          "unused",
		},
		{
			name:          "warnStaleDays=0 disables classification",
			createdAt:     rfc(1000 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(900 * 24 * time.Hour),
			warnStaleDays: 0,
			want:          "active",
		},
		{
			name:          "negative warnStaleDays clamps to disabled",
			createdAt:     rfc(1000 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(900 * 24 * time.Hour),
			warnStaleDays: -1,
			want:          "active",
		},
		{
			name:          "malformed last_used_at defaults to active (server bug, not user fault)",
			createdAt:     rfc(2 * 24 * time.Hour),
			lastUsedAt:    strPtr("not-a-timestamp"),
			warnStaleDays: 90,
			want:          "active",
		},
		{
			name:          "malformed created_at defaults to active when never used",
			createdAt:     "not-a-timestamp",
			lastUsedAt:    nil,
			warnStaleDays: 90,
			want:          "active",
		},
		{
			name:          "1-day threshold catches a 2-day-stale token",
			createdAt:     rfc(10 * 24 * time.Hour),
			lastUsedAt:    rfcPtr(2 * 24 * time.Hour),
			warnStaleDays: 1,
			want:          "stale",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyTokenStatus(tc.createdAt, tc.lastUsedAt, tc.revokedAt, tc.warnStaleDays, now)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTokenListFlag_WarnStaleDays guards the flag wiring on the
// command — a refactor that drops the flag would silently regress
// the documented staleness check.
//
// NOT parallel: inspects the package-level tokenListCmd FlagSet,
// which other tests in the package may mutate concurrently. Cost
// of running serially is sub-millisecond.
func TestTokenListFlag_WarnStaleDays(t *testing.T) {
	f := tokenListCmd.Flags().Lookup("warn-stale-days")
	if f == nil {
		t.Fatal("crewship token list missing --warn-stale-days flag")
	}
	if f.Value.Type() != "int" {
		t.Errorf("--warn-stale-days type = %s, want int", f.Value.Type())
	}
	if f.DefValue != "90" {
		t.Errorf("--warn-stale-days default = %s, want 90", f.DefValue)
	}
}

func strPtr(s string) *string { return &s }
