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
