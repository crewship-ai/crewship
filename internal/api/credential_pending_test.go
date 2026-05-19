package api

import "testing"

// TestIsPendingSentinel verifies the defence-in-depth check that
// keeps placeholder credential values from reaching the agent's
// env. The SQL status='ACTIVE' filter on every resolver query is
// the primary guard; this test pins the post-decrypt bailout so a
// future query that forgets the filter still doesn't leak.
func TestIsPendingSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"oauth-sentinel", pendingSentinelOAuth, true},
		{"manifest-sentinel", pendingSentinelManifest, true},
		{"empty-string", "", false},
		{"real-api-key", "sk-ant-real-key", false},
		// User-typed plausible secrets that resemble the OLD
		// human-readable sentinels must NOT collide.
		{"old-oauth-shape", "pending_oauth", false},
		{"old-manifest-shape", "pending_manifest", false},
		{"prefix-only-must-not-match", "pending_", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPendingSentinel(tc.in); got != tc.want {
				t.Errorf("isPendingSentinel(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
