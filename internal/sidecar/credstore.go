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

// Select picks the next active credential for a provider using round-robin.
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

	idx := cs.idx[provider] % len(candidates)
	cs.idx[provider] = idx + 1

	result := cs.creds[candidates[idx]]
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
