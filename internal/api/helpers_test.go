package api

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParsePagination_Clamping is a regression for a CodeRabbit finding on
// PR #130: `limit > maxLimit` used to fall through the same branch as
// `limit <= 0` and get reset to defaultLimit, which silently shifted the
// pagination window instead of clamping to maxLimit as the godoc promised.
func TestParsePagination_Clamping(t *testing.T) {
	cases := []struct {
		name                   string
		query                  string
		defaultLimit, maxLimit int
		wantLimit, wantOffset  int
	}{
		{"unspecified uses default", "", 20, 100, 20, 0},
		{"in-range passes through", "?limit=30&offset=10", 20, 100, 30, 10},
		{"over-max clamps to max (not default)", "?limit=1000", 20, 100, 100, 0},
		{"exactly max", "?limit=100", 20, 100, 100, 0},
		{"one above max", "?limit=101", 20, 100, 100, 0},
		{"zero falls back to default", "?limit=0", 20, 100, 20, 0},
		{"negative falls back to default", "?limit=-5", 20, 100, 20, 0},
		{"non-numeric falls back to default", "?limit=abc", 20, 100, 20, 0},
		{"negative offset clamped to zero", "?offset=-10", 20, 100, 20, 0},
		{"both clamped", "?limit=99999&offset=-1", 50, 200, 200, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x"+tc.query, nil)
			gotLimit, gotOffset := parsePagination(req, tc.defaultLimit, tc.maxLimit)
			if gotLimit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", gotLimit, tc.wantLimit)
			}
			if gotOffset != tc.wantOffset {
				t.Errorf("offset = %d, want %d", gotOffset, tc.wantOffset)
			}
		})
	}
}

func TestIsSafeRedirect(t *testing.T) {
	tests := []struct {
		input string
		safe  bool
	}{
		{"/", true},
		{"/dashboard", true},
		{"/settings?tab=profile", true},
		{"/path/to/page#anchor", true},
		{"", false},
		{"https://evil.com", false},
		{"http://evil.com", false},
		{"//evil.com", false},
		{"//evil.com/path", false},
		{`/foo\bar`, false},
		{`\/evil.com`, false},
		{"ftp://evil.com", false},
		{"javascript:alert(1)", false},
		{"relative/path", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.safe, isSafeRedirect(tt.input))
		})
	}
}

// TestCanRole locks down the role × action matrix. The original switch only
// recognised create/manage/read, so two production handlers (CacheDelete with
// "delete", RestartCrewAgents with "update") fell through to default and
// always returned 403. This table guarantees both actions are honoured and
// fails loudly if anyone trims the switch back.
func TestCanRole(t *testing.T) {
	cases := []struct {
		role   string
		action string
		want   bool
	}{
		// read — any authenticated role
		{"OWNER", "read", true},
		{"ADMIN", "read", true},
		{"MANAGER", "read", true},
		{"MEMBER", "read", true},
		{"VIEWER", "read", true},
		{"", "read", true}, // empty role still passes read; auth middleware enforces presence

		// create — OWNER/ADMIN/MANAGER
		{"OWNER", "create", true},
		{"ADMIN", "create", true},
		{"MANAGER", "create", true},
		{"MEMBER", "create", false},
		{"VIEWER", "create", false},
		{"", "create", false},

		// update — same tier as create (reversible mutations)
		{"OWNER", "update", true},
		{"ADMIN", "update", true},
		{"MANAGER", "update", true},
		{"MEMBER", "update", false},
		{"VIEWER", "update", false},
		{"", "update", false},

		// manage — OWNER/ADMIN
		{"OWNER", "manage", true},
		{"ADMIN", "manage", true},
		{"MANAGER", "manage", false},
		{"MEMBER", "manage", false},
		{"VIEWER", "manage", false},

		// delete — same tier as manage (destructive)
		{"OWNER", "delete", true},
		{"ADMIN", "delete", true},
		{"MANAGER", "delete", false},
		{"MEMBER", "delete", false},
		{"VIEWER", "delete", false},

		// Unknown actions must deny — fail-closed by design.
		{"OWNER", "wat", false},
		{"OWNER", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.role+"/"+tc.action, func(t *testing.T) {
			if got := canRole(tc.role, tc.action); got != tc.want {
				t.Errorf("canRole(%q, %q) = %v, want %v", tc.role, tc.action, got, tc.want)
			}
		})
	}

	// Multi-action: caller must satisfy ALL of them.
	t.Run("multi-action AND semantics", func(t *testing.T) {
		if !canRole("OWNER", "read", "manage", "delete") {
			t.Error("OWNER should satisfy read+manage+delete")
		}
		if canRole("MANAGER", "create", "delete") {
			t.Error("MANAGER must NOT satisfy delete; multi-action should AND")
		}
		if canRole("MEMBER", "read", "create") {
			t.Error("MEMBER must NOT satisfy create even when read passes")
		}
	})
}
