package api

import (
	"net/http/httptest"
	"testing"
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
