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
		{"oauth-sentinel", "pending_oauth", true},
		{"manifest-sentinel", "pending_manifest", true},
		{"empty-string", "", false},
		{"real-api-key", "sk-ant-real-key", false},
		{"prefix-only-must-not-match", "pending_", false},
		{"superset-must-not-match", "pending_oauth_token", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPendingSentinel(tc.in); got != tc.want {
				t.Errorf("isPendingSentinel(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
