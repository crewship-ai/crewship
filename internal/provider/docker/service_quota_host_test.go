package docker

import (
	"errors"
	"strings"
	"testing"
)

// Docker silently records -1 for MemorySwap / PidsLimit on hosts without
// swap or PID cgroup accounting. A quota service there would fail its own
// drift audit on every ensure and be recreated forever, so it is refused up
// front with an actionable error, before any disk is allocated.
func TestQuotaServiceRefusedWithoutHostAccounting(t *testing.T) {
	cases := []struct {
		name            string
		swap, pids      bool
		wantErrContains string
	}{
		{name: "supported host", swap: true, pids: true},
		{name: "no swap accounting", pids: true, wantErrContains: "swap"},
		{name: "no pid accounting", swap: true, wantErrContains: "PID"},
		{name: "neither", wantErrContains: "swap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemon := newFakeQuotaDaemon(t)
			daemon.swapLimit, daemon.pidsLimit = tc.swap, tc.pids
			daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}}}
			catalog := &fakeQuotaCatalog{}
			svc := quotaTestService()
			p := newCovProvider(t, Config{QuotaCatalog: catalog}, daemon.ServeHTTP)
			_, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", p.crewNetworkFor(covCrewID, "alpha"), &svc)
			if tc.wantErrContains == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErrContains) || !errors.Is(err, errQuotaHostUnsupported) {
				t.Fatalf("unsupported host not refused clearly: %v", err)
			}
			if daemon.mutatingCalls() != 0 || len(catalog.owners) != 0 {
				t.Fatalf("refused service still allocated or touched Docker: calls %d ensures %d", daemon.mutatingCalls(), len(catalog.owners))
			}
		})
	}
}

// Non-opt-in services do not depend on swap/PID accounting.
func TestLegacyServiceIgnoresHostAccounting(t *testing.T) {
	daemon := newFakeQuotaDaemon(t)
	daemon.swapLimit, daemon.pidsLimit = false, false
	svc := covRedisSvc()
	p := newCovProvider(t, Config{}, daemon.ServeHTTP)
	if _, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", p.crewNetworkFor(covCrewID, "alpha"), &svc); err != nil {
		t.Fatal(err)
	}
}
