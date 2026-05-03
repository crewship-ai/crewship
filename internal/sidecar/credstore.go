package sidecar

import (
	"sync"
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
	idx   map[ProviderType]int // round-robin index per provider
}

// NewCredStore creates an empty credential store.
func NewCredStore() *CredStore {
	return &CredStore{
		idx: make(map[ProviderType]int),
	}
}

// Load replaces all credentials in the store.
func (cs *CredStore) Load(creds []Credential) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.creds = make([]Credential, len(creds))
	copy(cs.creds, creds)
	cs.idx = make(map[ProviderType]int)
}

// Select picks the next active credential for a provider.
// Credentials are grouped by Priority (lower = higher priority).
// Within the highest-priority tier, round-robin rotation is used.
// Returns nil if no credential is available.
func (cs *CredStore) Select(provider ProviderType) *Credential {
	cs.mu.Lock()
	defer cs.mu.Unlock()

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

	target := cs.idx[provider] % topCount
	cs.idx[provider] = target + 1

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
