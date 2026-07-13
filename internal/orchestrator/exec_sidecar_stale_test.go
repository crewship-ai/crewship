package orchestrator

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestCheckSidecar_StaleDetection locks #1008: when the container provider can
// report the on-disk sidecar build hash and it differs from the hash the
// running sidecar advertises on /health, checkSidecar flags the result Stale
// (the container is serving an OLD bind-mounted sidecar after a redeploy). It
// must FAIL OPEN — never flag stale — when either hash is unknown, so a
// pre-#1008 sidecar (empty hash) or a provider that can't report never trips a
// false alarm.
func TestCheckSidecar_StaleDetection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		runningHash  string // hash the sidecar reports on /health
		expectedHash string // hash the provider says is on disk ("" = can't report)
		wantStale    bool
	}{
		{name: "match → fresh", runningHash: "aaaa", expectedHash: "aaaa", wantStale: false},
		{name: "mismatch → stale", runningHash: "aaaa", expectedHash: "bbbb", wantStale: true},
		{name: "provider can't report → fail-open", runningHash: "aaaa", expectedHash: "", wantStale: false},
		{name: "sidecar reports no hash → fail-open", runningHash: "", expectedHash: "bbbb", wantStale: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := `{"status":"ok","network_mode":"free","sidecar_hash":"` + tc.runningHash + `"}`
			c := &covContainer{
				expectedHash: tc.expectedHash,
				route: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
					return covResult("health", body), nil
				},
			}
			h := checkSidecar(context.Background(), c, "ctr1")
			if h == nil {
				t.Fatal("expected non-nil health for an ok sidecar")
			}
			if h.Stale != tc.wantStale {
				t.Errorf("Stale = %v, want %v (running=%q expected=%q)", h.Stale, tc.wantStale, tc.runningHash, tc.expectedHash)
			}
			if h.SidecarHash != tc.runningHash {
				t.Errorf("SidecarHash = %q, want %q", h.SidecarHash, tc.runningHash)
			}
		})
	}
}
