package sidecar

import (
	"sync"
	"sync/atomic"
	"time"
)

// ProviderType identifies an LLM API provider.
type ProviderType string

const (
	ProviderAnthropic ProviderType = "ANTHROPIC"
	ProviderOpenAI    ProviderType = "OPENAI"
	ProviderGoogle    ProviderType = "GOOGLE"
	// ProviderCursor — added with the multi-CLI adapter wave. The sidecar
	// reverse-proxy currently only injects keys for Anthropic; Cursor (and
	// OpenAI/Google) are routed via direct env-var injection in
	// BuildEnvVarsSidecar instead. ProviderCursor exists so credstore can
	// still report counts and so future proxy wiring has a stable identifier.
	ProviderCursor ProviderType = "CURSOR"
	// ProviderFactory — Factory Droid (droid exec). Same direct-env-var
	// injection model as Cursor; sidecar reverse-proxy not wired yet.
	ProviderFactory ProviderType = "FACTORY"
	// ProviderOpenRouter — OpenRouter, reached through the reverse proxy at
	// /llm/openrouter. Unlike Cursor/Factory this one IS proxy-routed: it has
	// an llmroute.Spec, so the key stays in the sidecar heap.
	ProviderOpenRouter ProviderType = "OPENROUTER"
	// ProviderOpenAICompat — a generic OpenAI-compatible endpoint (self-hosted
	// a self-hosted runtime or an inference vendor). The upstream host comes from the
	// credential itself (Credential.BaseURL), which is why it is the only
	// provider whose dial target is not a compile-time constant — see the
	// allowlist + SSRF gates in reverseProxyToProvider.
	ProviderOpenAICompat ProviderType = "OPENAI_COMPAT"
)

// Credential holds a decrypted credential for injection into outbound requests.
type Credential struct {
	ID       string       `json:"id"`
	Provider ProviderType `json:"provider"`
	Token    string       `json:"token"`
	Priority int          `json:"priority"`
	// LeaseExpiresAt is the grant's credential-lease deadline in RFC3339 UTC,
	// carried in the boot payload from the server-side grant that delivered this
	// credential (#1373). Empty means a STANDING grant with no expiry — the
	// default, and the behaviour of every credential before leases existed.
	//
	// It travels with the credential rather than being polled because boot
	// delivery is credential-scoped (one crew-wide CredStore keyed by credential
	// id) while a lease is grant-scoped (per agent). The crew-wide listing the
	// revocation reaper polls has no per-agent dimension and a workspace-scoped
	// credential passes its visibility filter regardless of any grant's TTL, so
	// the server cannot express "this delivery was leased" through that channel.
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`

	// BaseURL is the operator-supplied upstream for a provider whose spec is
	// UpstreamFromCredential (today: OPENAI_COMPAT). Empty for every built-in
	// provider, whose upstream host is a compile-time constant in the
	// descriptor. `omitempty` is load-bearing: a credential without it must
	// serialise byte-identically to the pre-phase-2 boot payload.
	BaseURL string `json:"base_url,omitempty"`

	// Headers are extra request headers the credential carries (an org / route
	// selector some gateways require). Written by ApplyAuth alongside the auth
	// slots, so they never reach the agent env. Empty for every built-in.
	Headers map[string]string `json:"headers,omitempty"`

	// leaseDeadline is LeaseExpiresAt parsed once at Load, so the hot Select path
	// does no time parsing. Zero value means "no lease" (standing). Unexported,
	// so it never round-trips through the boot JSON.
	leaseDeadline time.Time
}

// leaseLapsed reports whether this credential's lease has expired as of now.
// A standing grant (zero deadline) never lapses. The comparison is "at or after
// the deadline is lapsed", mirroring the server-side gate (expires_at > now) so
// the two sides agree on the exact instant a lease dies.
func (c *Credential) leaseLapsed(now time.Time) bool {
	return !c.leaseDeadline.IsZero() && !now.Before(c.leaseDeadline)
}

// leaseEpochSentinel is the deadline assigned to a credential whose
// LeaseExpiresAt could not be parsed. The server always writes a fixed-width
// RFC3339 UTC value, so an unparseable one means corruption — and for a security
// control the safe reading of "I cannot tell when this expires" is "it already
// did", not "it never does".
var leaseEpochSentinel = time.Unix(0, 0).UTC()

// CredStore holds credentials in memory. Never written to disk.
// Safe for concurrent use.
type CredStore struct {
	mu    sync.RWMutex
	creds []Credential
	// rr holds the per-provider round-robin counter (ProviderType -> *uint64),
	// bumped with atomic.AddUint64 so Select only needs a READ lock on the hot
	// path — no write-lock serialization across concurrent outbound requests
	// (#1081). A sync.Map lets the counter be created lazily for a provider
	// without a map write under the read lock.
	rr sync.Map
}

// NewCredStore creates an empty credential store.
func NewCredStore() *CredStore {
	return &CredStore{}
}

// Load replaces all credentials in the store, parsing each one's lease deadline
// (#1373) so Select can enforce it without re-parsing per request.
func (cs *CredStore) Load(creds []Credential) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.creds = make([]Credential, len(creds))
	copy(cs.creds, creds)
	for i := range cs.creds {
		raw := cs.creds[i].LeaseExpiresAt
		if raw == "" {
			cs.creds[i].leaseDeadline = time.Time{} // standing grant
			continue
		}
		if d, err := time.Parse(time.RFC3339, raw); err == nil {
			cs.creds[i].leaseDeadline = d
		} else {
			// Fail closed — see leaseEpochSentinel.
			cs.creds[i].leaseDeadline = leaseEpochSentinel
		}
	}
	// Restart round-robin from the top on a reload (matches the previous
	// idx-map reset). Safe under the write lock held here; no Select can be
	// mid-flight because Select holds the read lock.
	cs.rr.Clear()
}

// Select picks the next active credential for a provider.
// Credentials are grouped by Priority (lower = higher priority).
// Within the highest-priority tier, round-robin rotation is used.
// Returns nil if no credential is available.
func (cs *CredStore) Select(provider ProviderType) *Credential {
	// READ lock only: the top-tier scan reads cs.creds, and round-robin now
	// advances an atomic per-provider counter (not a map index), so concurrent
	// Selects don't serialize on a write lock (#1081). Load/Remove/Reap still
	// take the write lock, so the creds slice can't change under us.
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	// #1373: a credential whose lease has lapsed is not selectable, and the
	// filter is applied in BOTH passes so a lapsed key neither inflates the tier
	// count nor shadows a usable sibling. Enforcing it here (rather than relying
	// on the 60s reaper sweep) makes expiry immediate instead of up to a full
	// interval late — the reaper's job is to stop the plaintext being resident,
	// not to be the gate.
	now := time.Now()

	// Pass 1: find the best (lowest-numeric) Priority for this provider and
	// count how many creds sit in that top tier. Done in a single scan instead
	// of building an intermediate `candidates` slice.
	bestPriority := 0
	topCount := 0
	for i := range cs.creds {
		c := &cs.creds[i]
		if c.Provider != provider || c.leaseLapsed(now) {
			continue
		}
		if topCount == 0 || c.Priority < bestPriority {
			bestPriority = c.Priority
			topCount = 1
		} else if c.Priority == bestPriority {
			topCount++
		}
	}
	if topCount == 0 {
		return nil
	}

	// Atomic round-robin within the top tier. LoadOrStore lazily creates the
	// counter for a provider on first use; AddUint64-1 yields a 0-based ticket
	// so the first Select maps to target 0 (unchanged from the old idx map).
	ctr, _ := cs.rr.LoadOrStore(provider, new(uint64))
	target := int((atomic.AddUint64(ctr.(*uint64), 1) - 1) % uint64(topCount))

	// Pass 2: iterate again and return the Nth match in the top tier. Scanning
	// in source-slice order is naturally stable (ascending original index) and
	// matches the previous `sort.Ints(topTier)` ordering exactly.
	seen := 0
	for i := range cs.creds {
		c := &cs.creds[i]
		if c.Provider != provider || c.Priority != bestPriority || c.leaseLapsed(now) {
			continue
		}
		if seen == target {
			result := *c
			return &result
		}
		seen++
	}
	return nil // unreachable: topCount > 0 guarantees a hit above.
}

// Remove removes a credential by ID (e.g. when revoked).
func (cs *CredStore) Remove(id string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	filtered := cs.creds[:0]
	removed := false
	for _, c := range cs.creds {
		if c.ID != id {
			filtered = append(filtered, c)
		} else {
			removed = true
		}
	}
	cs.creds = filtered
	if removed {
		// Mirrors Reap: a shrunk tier can leave a stale round-robin counter
		// pointing past the end (self-corrects via modulo, but resetting
		// keeps distribution clean) — kept consistent with Reap's Clear()
		// rather than silently diverging (#1139 review nit).
		cs.rr.Clear()
	}
}

// Reap removes every credential whose ID is NOT in keep, returning how many
// were removed. It is the revocation-reaper's primitive: the sidecar has no
// plaintext supply line after boot, so we never re-add or replace tokens — we
// only drop the ones crewshipd no longer lists as live (revoked/deleted). A nil
// or empty keep set is treated literally (removes everything); callers must only
// invoke this after a SUCCESSFUL fetch so a transient error can't nuke valid
// keys.
func (cs *CredStore) Reap(keep map[string]struct{}) int {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	filtered := cs.creds[:0]
	removed := 0
	for _, c := range cs.creds {
		if _, ok := keep[c.ID]; ok {
			filtered = append(filtered, c)
		} else {
			removed++
		}
	}
	cs.creds = filtered
	if removed > 0 {
		// Round-robin counters may now point past the end of a shrunk tier.
		// (Select's modulo self-corrects, but resetting keeps distribution
		// clean after a reap.) Safe under the write lock held here.
		cs.rr.Clear()
	}
	return removed
}

// ExpireLeases removes every credential whose lease has lapsed as of now,
// returning how many were dropped (#1373). This is the reaper's lease primitive,
// and it is deliberately independent of the revocation reaper's server fetch:
//
//   - Revocation is fail-OPEN. The sidecar cannot know about a revocation without
//     asking crewshipd, so a transient fetch failure must keep the current keys
//     (see reapRevokedCredentials) or a blip would nuke a working agent.
//   - Lease expiry is fail-CLOSED. The deadline was delivered with the
//     credential, so no round-trip is needed and an unreachable server is no
//     excuse for continuing to serve a lapsed lease.
//
// Select already refuses a lapsed lease, so this is not the correctness gate —
// it is what stops the expired plaintext from staying resident in the process.
func (cs *CredStore) ExpireLeases(now time.Time) int {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	filtered := cs.creds[:0]
	dropped := 0
	for _, c := range cs.creds {
		if c.leaseLapsed(now) {
			dropped++
			continue
		}
		filtered = append(filtered, c)
	}
	cs.creds = filtered
	if dropped > 0 {
		// Mirrors Reap/Remove: a shrunk tier can leave a stale round-robin
		// counter pointing past the end.
		cs.rr.Clear()
	}
	return dropped
}

// Count returns the number of credentials for a provider.
func (cs *CredStore) Count(provider ProviderType) int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	n := 0
	for _, c := range cs.creds {
		if c.Provider == provider {
			n++
		}
	}
	return n
}

// CountsByProvider returns one count per provider present in the store, in a
// SINGLE pass. /health used to call Count once per provider, which was fine at
// three hardcoded providers and stops being fine once the surface is
// descriptor-driven (one O(n) scan and one RLock acquisition per registered
// spec, on a handler the orchestrator polls). Providers with no credentials
// are absent from the map — the caller decides whether a missing key means
// "zero" (it does) or "not advertised".
func (cs *CredStore) CountsByProvider() map[string]int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	out := make(map[string]int, 4)
	for _, c := range cs.creds {
		out[string(c.Provider)]++
	}
	return out
}
