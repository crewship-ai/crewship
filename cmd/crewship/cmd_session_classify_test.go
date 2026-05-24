package main

import (
	"testing"
	"time"
)

func TestClassifySessionStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	rfc := func(d time.Duration) string {
		return now.Add(-d).Format(time.RFC3339)
	}

	cases := []struct {
		name          string
		isCurrent     bool
		lastUsedAt    string
		warnStaleDays int
		want          string
	}{
		{
			name:          "current wins even when last_used_at is ancient",
			isCurrent:     true,
			lastUsedAt:    rfc(365 * 24 * time.Hour),
			warnStaleDays: 30,
			want:          "current",
		},
		{
			name:          "current wins with empty last_used_at",
			isCurrent:     true,
			lastUsedAt:    "",
			warnStaleDays: 30,
			want:          "current",
		},
		{
			name:          "fresh non-current → active",
			isCurrent:     false,
			lastUsedAt:    rfc(1 * time.Hour),
			warnStaleDays: 30,
			want:          "active",
		},
		{
			name:          "29 days non-current → active (under threshold)",
			isCurrent:     false,
			lastUsedAt:    rfc(29 * 24 * time.Hour),
			warnStaleDays: 30,
			want:          "active",
		},
		{
			name:          "exactly at threshold → active (strict greater-than is stale)",
			isCurrent:     false,
			lastUsedAt:    rfc(30 * 24 * time.Hour),
			warnStaleDays: 30,
			want:          "active",
		},
		{
			name:          "over threshold → stale",
			isCurrent:     false,
			lastUsedAt:    rfc(31 * 24 * time.Hour),
			warnStaleDays: 30,
			want:          "stale",
		},
		{
			name:          "far over threshold → stale",
			isCurrent:     false,
			lastUsedAt:    rfc(180 * 24 * time.Hour),
			warnStaleDays: 30,
			want:          "stale",
		},
		{
			name:          "warnStaleDays=0 disables check",
			isCurrent:     false,
			lastUsedAt:    rfc(365 * 24 * time.Hour),
			warnStaleDays: 0,
			want:          "active",
		},
		{
			name:          "warnStaleDays=-1 clamps to disabled",
			isCurrent:     false,
			lastUsedAt:    rfc(365 * 24 * time.Hour),
			warnStaleDays: -1,
			want:          "active",
		},
		{
			name:          "malformed last_used_at → active (server bug fallback)",
			isCurrent:     false,
			lastUsedAt:    "not-a-timestamp",
			warnStaleDays: 30,
			want:          "active",
		},
		{
			name:          "empty last_used_at (non-current) → active (no signal to mark stale)",
			isCurrent:     false,
			lastUsedAt:    "",
			warnStaleDays: 30,
			want:          "active",
		},
		{
			name:          "current + warnStaleDays=0 → current",
			isCurrent:     true,
			lastUsedAt:    rfc(365 * 24 * time.Hour),
			warnStaleDays: 0,
			want:          "current",
		},
		{
			name:          "1-day threshold catches a 2-day-stale session",
			isCurrent:     false,
			lastUsedAt:    rfc(2 * 24 * time.Hour),
			warnStaleDays: 1,
			want:          "stale",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifySessionStatus(tc.isCurrent, tc.lastUsedAt, tc.warnStaleDays, now)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionListFlag_WarnStaleDays(t *testing.T) {
	t.Parallel()
	f := sessionListCmd.Flags().Lookup("warn-stale-days")
	if f == nil {
		t.Fatal("crewship session list missing --warn-stale-days flag")
	}
	if f.Value.Type() != "int" {
		t.Errorf("--warn-stale-days type = %s, want int", f.Value.Type())
	}
	if f.DefValue != "30" {
		t.Errorf("--warn-stale-days default = %s, want 30", f.DefValue)
	}
}
