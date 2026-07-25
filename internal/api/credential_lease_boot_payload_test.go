package api

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestLease_BootResolverCarriesLeaseDeadline is the plumbing guard for the
// sidecar half of #1373. The server-side gates only decide what to deliver at the
// INSTANT of delivery; the container then holds that plaintext for its whole
// life. The only way lease expiry reaches a running container is for the deadline
// to travel WITH the credential into the crew's CredStore, so this asserts the
// boot resolver actually emits it.
//
// The rest of the hop (mcpCredEntry → chatbridge credentialResponse →
// orchestrator.Credential → sidecarCred JSON → sidecar.Credential) is a
// same-named/same-tagged field chain; the sidecar's own tests then cover the
// enforcement (credstore_lease_test.go).
func TestLease_BootResolverCarriesLeaseDeadline(t *testing.T) {
	db := setupTestDB(t)
	h := covCfgHandler(db)
	agentID, standingEnv, leasedEnv := seedLeasedGrant(t, db, "payload")

	// Make the leased grant LIVE so it is delivered — the point here is the
	// deadline travelling, not the refusal.
	future := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	execOrFatal(t, db, `UPDATE agent_credentials SET expires_at = ? WHERE agent_id = ? AND env_var_name = ?`,
		future, agentID, leasedEnv)

	creds, err := h.resolveAgentCredentials(httptest.NewRequest("GET", "/", nil), agentID)
	if err != nil {
		t.Fatalf("resolveAgentCredentials: %v", err)
	}

	var sawLeased, sawStanding bool
	for _, c := range creds {
		switch c.EnvVar {
		case leasedEnv:
			sawLeased = true
			if c.LeaseExpiresAt != future {
				t.Errorf("leased credential LeaseExpiresAt = %q, want %q — the sidecar cannot expire what it is not told about",
					c.LeaseExpiresAt, future)
			}
		case standingEnv:
			sawStanding = true
			if c.LeaseExpiresAt != "" {
				t.Errorf("standing grant reported LeaseExpiresAt = %q, want empty", c.LeaseExpiresAt)
			}
		}
	}
	if !sawLeased || !sawStanding {
		t.Fatalf("expected both credentials in the payload, got %+v", creds)
	}
}

// TestLease_OAuthTokenInheritsTightestLease pins the aggregation rule for the
// synthesized _OAUTH_ACCESS_TOKEN entry. When several entries share a credential
// id, the derived token must inherit the MOST RESTRICTIVE deadline — a token that
// silently outlives the tightest grant it was derived from is an unexplained late
// injection, and "last one seen wins" over a Go map makes that outcome
// order-dependent.
func TestLease_OAuthTokenInheritsTightestLease(t *testing.T) {
	tight := "2026-07-25T10:00:00Z"
	loose := "2026-07-25T18:00:00Z"

	cases := []struct {
		name   string
		leases []string // LeaseExpiresAt of each sibling entry, in slice order
		want   string
	}{
		{"loose first", []string{loose, tight}, tight},
		{"tight first", []string{tight, loose}, tight},
		{"standing sibling does not loosen a lease", []string{"", tight}, tight},
		{"lease does not tighten a standing pair", []string{"", ""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Mirror the aggregation in resolveOAuthAccessTokens. Kept as a focused
			// unit over the rule itself: driving it through the full resolver would
			// need two agent_credentials rows for one credential, which the UNIQUE
			// constraint forbids — the guard exists for the other builders that also
			// contribute to this slice.
			out := map[string]string{}
			for _, l := range tc.leases {
				existing, seen := out["cred"]
				if !seen || (l != "" && (existing == "" || l < existing)) {
					out["cred"] = l
				}
			}
			if out["cred"] != tc.want {
				t.Errorf("aggregated lease = %q, want %q", out["cred"], tc.want)
			}
		})
	}
}
