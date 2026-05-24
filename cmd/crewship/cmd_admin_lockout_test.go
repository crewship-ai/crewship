//go:build !clionly

package main

import (
	"strings"
	"testing"
	"time"
)

func TestClassifyLockoutStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	rfc := func(d time.Duration) string {
		return now.Add(d).Format(time.RFC3339)
	}

	cases := []struct {
		name        string
		raw         string
		wantActive  bool
		mustContain string
	}{
		{
			name:        "empty → not locked",
			raw:         "",
			wantActive:  false,
			mustContain: "-",
		},
		{
			name:        "whitespace-only → not locked",
			raw:         "   ",
			wantActive:  false,
			mustContain: "-",
		},
		{
			name:        "future RFC3339 → active",
			raw:         rfc(30 * time.Minute),
			wantActive:  true,
			mustContain: "LOCKED until",
		},
		{
			name:        "far-future → active",
			raw:         rfc(7 * 24 * time.Hour),
			wantActive:  true,
			mustContain: "LOCKED until",
		},
		{
			name:        "past RFC3339 → not locked, shows expired",
			raw:         rfc(-30 * time.Minute),
			wantActive:  false,
			mustContain: "expired",
		},
		{
			name:        "exactly now → not locked (boundary: After, not !Before)",
			raw:         rfc(0),
			wantActive:  false,
			mustContain: "expired",
		},
		{
			name:        "SQLite space-separator format (future) → active",
			raw:         now.Add(15 * time.Minute).UTC().Format("2006-01-02 15:04:05"),
			wantActive:  true,
			mustContain: "LOCKED until",
		},
		{
			name:        "SQLite space-separator (past) → not locked",
			raw:         now.Add(-15 * time.Minute).UTC().Format("2006-01-02 15:04:05"),
			wantActive:  false,
			mustContain: "expired",
		},
		{
			name:        "malformed → not active, raw passes through",
			raw:         "not-a-timestamp",
			wantActive:  false,
			mustContain: "not-a-timestamp",
		},
		{
			name:        "malformed with leading whitespace → trimmed, raw shows trimmed form",
			raw:         "  bogus  ",
			wantActive:  false,
			mustContain: "bogus",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			active, display := classifyLockoutStatus(tc.raw, now)
			if active != tc.wantActive {
				t.Errorf("active = %v, want %v (display=%q)", active, tc.wantActive, display)
			}
			if !strings.Contains(display, tc.mustContain) {
				t.Errorf("display = %q, want substring %q", display, tc.mustContain)
			}
		})
	}
}

// TestAdminListUsersFlag_LockedOnly guards the flag wiring on the
// admin list-users command so a refactor that drops the flag would
// silently regress the documented filter.
func TestAdminListUsersFlag_LockedOnly(t *testing.T) {
	t.Parallel()
	f := adminListUsersCmd.Flags().Lookup("locked-only")
	if f == nil {
		t.Fatal("crewship admin list-users missing --locked-only flag")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--locked-only type = %s, want bool", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--locked-only default = %s, want false", f.DefValue)
	}
}
