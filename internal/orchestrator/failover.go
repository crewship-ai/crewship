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

// IsInCooldown reports whether the credential is currently in a cooldown period.
func (cm *CooldownManager) IsInCooldown(credID string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	until, ok := cm.cooldowns[credID]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		return false
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
