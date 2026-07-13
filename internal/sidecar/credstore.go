package sidecar

import (
	"sync"
	"sync/atomic"
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
)

// Credential holds a decrypted credential for injection into outbound requests.
type Credential struct {
	ID       string       `json:"id"`
	Provider ProviderType `json:"provider"`
	Token    string       `json:"token"`
	Priority int          `json:"priority"`
}

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

// Load replaces all credentials in the store.
func (cs *CredStore) Load(creds []Credential) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.creds = make([]Credential, len(creds))
	copy(cs.creds, creds)
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

	// Pass 1: find the best (lowest-numeric) Priority for this provider and
	// count how many creds sit in that top tier. Done in a single scan instead
	// of building an intermediate `candidates` slice.
	bestPriority := 0
	topCount := 0
	for i := range cs.creds {
		c := &cs.creds[i]
		if c.Provider != provider {
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
		if c.Provider != provider || c.Priority != bestPriority {
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
	for _, c := range cs.creds {
		if c.ID != id {
			filtered = append(filtered, c)
		}
	}
	cs.creds = filtered
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
