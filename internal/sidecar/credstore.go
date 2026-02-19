package sidecar

import (
	"sort"
	"sync"
)

// ProviderType identifies an LLM API provider.
type ProviderType string

const (
	ProviderAnthropic ProviderType = "ANTHROPIC"
	ProviderOpenAI    ProviderType = "OPENAI"
	ProviderGoogle    ProviderType = "GOOGLE"
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

	var candidates []int
	for i, c := range cs.creds {
		if c.Provider == provider {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// Find the highest priority (lowest numeric value) among candidates
	bestPriority := cs.creds[candidates[0]].Priority
	for _, idx := range candidates[1:] {
		if cs.creds[idx].Priority < bestPriority {
			bestPriority = cs.creds[idx].Priority
		}
	}

	// Filter to only the top-priority tier
	var topTier []int
	for _, idx := range candidates {
		if cs.creds[idx].Priority == bestPriority {
			topTier = append(topTier, idx)
		}
	}

	// Stable ordering within tier for deterministic round-robin
	sort.Ints(topTier)

	rrIdx := cs.idx[provider] % len(topTier)
	cs.idx[provider] = rrIdx + 1

	result := cs.creds[topTier[rrIdx]]
	return &result
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
