package orchestrator

import (
	"strings"
	"sync"
	"time"
)

type CooldownManager struct {
	cooldowns map[string]time.Time
	mu        sync.RWMutex
}

func NewCooldownManager() *CooldownManager {
	return &CooldownManager{
		cooldowns: make(map[string]time.Time),
	}
}

func (cm *CooldownManager) MarkCooldown(credID string, duration time.Duration) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cooldowns[credID] = time.Now().Add(duration)
}

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

func IsRateLimitError(exitCode int, stderr string) bool {
	if exitCode != 1 {
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
