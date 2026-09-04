package sidecar

import (
	"testing"
	"time"
)

// Credential-lease enforcement inside the sidecar (#1373).
//
// Boot delivery is CREDENTIAL-scoped: the crew sidecar holds one CredStore keyed
// by credential id, shared by every agent in the crew. Leases are GRANT-scoped
// (per agent, per credential). That mismatch is why the server-side crew listing
// the revocation reaper polls cannot express lease expiry — a crew-wide listing
// has no per-agent dimension, and a workspace-scoped credential passes its
// visibility OR regardless of any grant's TTL.
//
// So the lease travels WITH the delivered credential as a deadline, and the
// sidecar enforces it locally. That inverts the failure mode deliberately:
// revocation is fail-OPEN (a crewshipd blip must not nuke working keys, the
// revoked key is reaped on the next good tick) while lease expiry is
// fail-CLOSED (it needs no round-trip, so an unreachable server is no excuse for
// serving a lapsed lease).

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// TestCredStore_SelectRefusesLapsedLease is the hard gate: Select must never
// hand out a credential whose lease has lapsed, with no dependency on the reaper
// having run. Without this, expiry would be up to a full 60s interval late.
func TestCredStore_SelectRefusesLapsedLease(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{
		ID:             "expired",
		Provider:       ProviderAnthropic,
		Token:          "sk-ant-expired",
		LeaseExpiresAt: rfc3339(time.Now().Add(-1 * time.Minute)),
	}})

	if got := cs.Select(ProviderAnthropic, ""); got != nil {
		t.Fatalf("Select returned a lapsed lease: %+v", got)
	}
}

// TestCredStore_SelectHonorsLiveLease: the availability half. A lease that has
// not lapsed must still be served, or enabling leases breaks every agent.
func TestCredStore_SelectHonorsLiveLease(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{
		ID:             "live",
		Provider:       ProviderAnthropic,
		Token:          "sk-ant-live",
		LeaseExpiresAt: rfc3339(time.Now().Add(1 * time.Hour)),
	}})

	got := cs.Select(ProviderAnthropic, "")
	if got == nil || got.ID != "live" {
		t.Fatalf("Select = %+v, want the live-lease credential", got)
	}
}

// TestCredStore_SelectHonorsStandingGrant: no lease field at all means a
// standing grant, which must be unaffected. This is the default for every
// credential today.
func TestCredStore_SelectHonorsStandingGrant(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "standing", Provider: ProviderAnthropic, Token: "sk-ant-standing"}})

	got := cs.Select(ProviderAnthropic, "")
	if got == nil || got.ID != "standing" {
		t.Fatalf("Select = %+v, want the standing credential", got)
	}
}

// TestCredStore_SelectSkipsLapsedAndFallsThrough: within one provider tier a
// lapsed lease must not shadow a usable sibling key. Getting this wrong turns a
// lease expiry into a total provider outage even though a valid key is loaded.
func TestCredStore_SelectSkipsLapsedAndFallsThrough(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "expired", Provider: ProviderAnthropic, Token: "a", Priority: 0,
			LeaseExpiresAt: rfc3339(time.Now().Add(-time.Minute))},
		{ID: "standing", Provider: ProviderAnthropic, Token: "b", Priority: 0},
	})

	// Several Selects so the round-robin visits both slots in the tier.
	for i := 0; i < 6; i++ {
		got := cs.Select(ProviderAnthropic, "")
		if got == nil {
			t.Fatalf("Select %d returned nil while a standing key is loaded", i)
		}
		if got.ID == "expired" {
			t.Fatalf("Select %d returned the lapsed lease", i)
		}
	}
}

// TestCredStore_UnparseableLeaseFailsClosed: the lease timestamp is written by
// the server in a fixed RFC3339 form, so a value that will not parse means
// corruption — not "no lease". Treat it as lapsed rather than serving the token.
func TestCredStore_UnparseableLeaseFailsClosed(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{
		ID: "garbage", Provider: ProviderAnthropic, Token: "a",
		LeaseExpiresAt: "not-a-timestamp",
	}})

	if got := cs.Select(ProviderAnthropic, ""); got != nil {
		t.Fatalf("Select served a credential with an unparseable lease: %+v", got)
	}
}

// TestCredStore_ExpireLeasesDropsLapsed is the reaper's primitive: lapsed leases
// leave the in-memory store entirely, so the plaintext stops being resident.
func TestCredStore_ExpireLeasesDropsLapsed(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "expired", Provider: ProviderAnthropic, Token: "a",
			LeaseExpiresAt: rfc3339(time.Now().Add(-time.Minute))},
		{ID: "live", Provider: ProviderAnthropic, Token: "b",
			LeaseExpiresAt: rfc3339(time.Now().Add(time.Hour))},
		{ID: "standing", Provider: ProviderOpenAI, Token: "c"},
	})

	if dropped := cs.ExpireLeases(time.Now()); dropped != 1 {
		t.Fatalf("ExpireLeases dropped %d, want 1", dropped)
	}
	if n := cs.Count(ProviderAnthropic); n != 1 {
		t.Errorf("Anthropic count = %d, want 1 (the live lease)", n)
	}
	if n := cs.Count(ProviderOpenAI); n != 1 {
		t.Errorf("OpenAI count = %d, want 1 (a standing grant is never expired)", n)
	}
	// Idempotent: a second sweep with nothing newly lapsed drops nothing.
	if dropped := cs.ExpireLeases(time.Now()); dropped != 0 {
		t.Errorf("second ExpireLeases dropped %d, want 0", dropped)
	}
}

// TestCredStore_ExpireLeasesAtTheDeadline pins the boundary as "at or after the
// deadline is lapsed", matching the server-side gate (expires_at > now), so the
// two sides can't disagree about the instant a lease dies.
func TestCredStore_ExpireLeasesAtTheDeadline(t *testing.T) {
	deadline := time.Now().UTC().Truncate(time.Second)
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "edge", Provider: ProviderAnthropic, Token: "a",
		LeaseExpiresAt: rfc3339(deadline)}})

	if dropped := cs.ExpireLeases(deadline.Add(-time.Second)); dropped != 0 {
		t.Fatalf("dropped %d one second BEFORE the deadline, want 0", dropped)
	}
	if dropped := cs.ExpireLeases(deadline); dropped != 1 {
		t.Fatalf("dropped %d AT the deadline, want 1", dropped)
	}
}

// TestCredStore_ReapKeepsLeaseDeadline: the revocation reaper rebuilds nothing —
// it only filters — so a credential that survives a Reap must keep enforcing its
// lease. A Reap that silently cleared the deadline would resurrect the exact
// standing-credential behaviour this closes.
func TestCredStore_ReapKeepsLeaseDeadline(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "leased", Provider: ProviderAnthropic, Token: "a",
		LeaseExpiresAt: rfc3339(time.Now().Add(-time.Minute))}})

	// crewshipd still lists it (the credential is not revoked), so Reap keeps it.
	if removed := cs.Reap(map[string]struct{}{"leased": {}}); removed != 0 {
		t.Fatalf("Reap removed %d, want 0 (credential is still live server-side)", removed)
	}
	if got := cs.Select(ProviderAnthropic, ""); got != nil {
		t.Fatalf("lease stopped being enforced after a Reap: %+v", got)
	}
}
