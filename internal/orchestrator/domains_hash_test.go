package orchestrator

// #1160: the orchestrator previously restarted a restricted-mode crew's
// sidecar UNCONDITIONALLY on every exec ("the domain allowlist may differ
// between agents, so we always restart to pick up the latest set"). With
// multiple agents sharing one crew container, that meant every OTHER
// agent's exec was a guaranteed kill+relaunch of an otherwise-healthy
// sidecar — and a live repro on dev3 (2026-07-15) showed the resulting
// churn producing repeated non-deploy-triggered "stale" false positives
// with no redeploy or container recreation in between.
//
// DomainsHash + sidecarNeedsRestart let the orchestrator skip the
// restart when the allowlist genuinely hasn't changed. DomainsHash cannot
// share code with internal/sidecar.DomainAllowlist.Hash() (sidecar imports
// orchestrator, so the reverse import would cycle) — it's reimplemented
// here and MUST stay byte-for-byte in lockstep: lower-case + dedupe via a
// set, sort, sha256, hex[:12].

import "testing"

func TestDomainsHash_OrderAndCaseInsensitive(t *testing.T) {
	t.Parallel()
	a := DomainsHash([]string{"api.anthropic.com", "api.openai.com"})
	b := DomainsHash([]string{"API.OPENAI.COM", "api.anthropic.com"})
	if a != b {
		t.Errorf("hash should be order/case-insensitive: a=%q b=%q", a, b)
	}
}

func TestDomainsHash_ChangesWithSet(t *testing.T) {
	t.Parallel()
	a := DomainsHash([]string{"api.anthropic.com", "api.openai.com"})
	c := DomainsHash([]string{"api.anthropic.com", "evil.com"})
	if a == c {
		t.Errorf("hash should differ when the domain set differs: a=%q c=%q", a, c)
	}
}

func TestDomainsHash_DuplicatesIgnored(t *testing.T) {
	t.Parallel()
	a := DomainsHash([]string{"api.anthropic.com", "api.openai.com"})
	dup := DomainsHash([]string{"api.anthropic.com", "api.openai.com", "api.openai.com"})
	if a != dup {
		t.Errorf("hash should ignore duplicate entries: a=%q dup=%q", a, dup)
	}
}

func TestSidecarNeedsRestart(t *testing.T) {
	t.Parallel()
	desiredDomains := []string{"api.anthropic.com", "api.openai.com"}
	matchingHash := DomainsHash(desiredDomains)

	cases := []struct {
		name        string
		health      *sidecarHealth
		desiredMode string
		want        bool
	}{
		{
			name:        "free mode, matches → no restart",
			health:      &sidecarHealth{NetworkMode: "free"},
			desiredMode: "free",
			want:        false,
		},
		{
			name:        "mode changed free→restricted → restart",
			health:      &sidecarHealth{NetworkMode: "free"},
			desiredMode: "restricted",
			want:        true,
		},
		{
			name:        "restricted, same domains → no restart (the #1160 fix)",
			health:      &sidecarHealth{NetworkMode: "restricted", DomainsHash: matchingHash},
			desiredMode: "restricted",
			want:        false,
		},
		{
			name:        "restricted, domains changed → restart",
			health:      &sidecarHealth{NetworkMode: "restricted", DomainsHash: "deadbeefcafe"},
			desiredMode: "restricted",
			want:        true,
		},
		{
			name:        "restricted, pre-#1160 sidecar (no domains_hash) → fail toward restart",
			health:      &sidecarHealth{NetworkMode: "restricted", DomainsHash: ""},
			desiredMode: "restricted",
			want:        true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sidecarNeedsRestart(tc.health, tc.desiredMode, desiredDomains); got != tc.want {
				t.Errorf("sidecarNeedsRestart() = %v, want %v", got, tc.want)
			}
		})
	}
}
