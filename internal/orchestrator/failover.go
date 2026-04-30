package orchestrator

import (
	"strings"
	"sync"
	"time"
)

// CooldownManager tracks credentials that are temporarily unavailable due to
// rate limiting, allowing the orchestrator to fail over to alternative credentials.
type CooldownManager struct {
	cooldowns map[string]time.Time
	mu        sync.RWMutex
}

// NewCooldownManager creates a new empty CooldownManager.
func NewCooldownManager() *CooldownManager {
	return &CooldownManager{
		cooldowns: make(map[string]time.Time),
	}
}

// MarkCooldown places a credential in cooldown for the given duration.
func (cm *CooldownManager) MarkCooldown(credID string, duration time.Duration) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cooldowns[credID] = time.Now().Add(duration)
}

// IsInCooldown reports whether the credential is currently in a cooldown
// period. Stale entries are pruned inline so the map can't grow unbounded
// over the process lifetime — without this self-prune, ClearExpired had
// no production caller and the map leaked one entry per rate-limited
// credential forever.
func (cm *CooldownManager) IsInCooldown(credID string) bool {
	cm.mu.RLock()
	until, ok := cm.cooldowns[credID]
	cm.mu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().After(until) {
		// Upgrade to a write lock just long enough to evict.
		cm.mu.Lock()
		defer cm.mu.Unlock()
		// Re-check under write lock so a concurrent MarkCooldown that
		// raced past the read isn't clobbered. If the entry was refreshed
		// to a future time between the RLock release and Lock acquire,
		// honor the refresh and report the credential as still in cooldown.
		cur, stillThere := cm.cooldowns[credID]
		if !stillThere {
			return false
		}
		if time.Now().After(cur) {
			delete(cm.cooldowns, credID)
			return false
		}
		return true
	}
	return true
}

// ClearExpired removes cooldown entries that have passed their expiry time.
func (cm *CooldownManager) ClearExpired() {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	now := time.Now()
	for id, until := range cm.cooldowns {
		if now.After(until) {
			delete(cm.cooldowns, id)
		}
	}
}

var rateLimitPatterns = []string{
	"rate limit",
	"rate_limit",
	"429",
	"too many requests",
	"quota exceeded",
	"insufficient_quota",
	"billing_hard_limit",
}

// IsRateLimitError checks whether the agent exit code and stderr indicate a
// rate limit or quota error from the LLM provider. Real-world CLI exits
// for a 429 vary by tool: 1 (generic error), 2 (usage), 124 (timeout
// after a long-running rate-limit retry), 137 (SIGKILL after the OOM
// killer kicked in on a stuck process). Any non-zero exit is acceptable
// as long as stderr carries one of the recognised rate-limit signals.
func IsRateLimitError(exitCode int, stderr string) bool {
	if exitCode == 0 {
		return false
	}
	lower := strings.ToLower(stderr)
	for _, p := range rateLimitPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
