package api

import (
	"sync"

	"github.com/crewship-ai/crewship/internal/config"
)

// pagePasswordAdmission has no waiting queue. Idle target keys are removed;
// rotating random tokens cannot retain more keys than admitted checks.
type pagePasswordAdmission struct {
	mu      sync.Mutex
	active  int
	targets map[string]int
	limits  config.PublicPagePasswordsConfig
}

func newPagePasswordAdmission(limits config.PublicPagePasswordsConfig) *pagePasswordAdmission {
	return &pagePasswordAdmission{targets: make(map[string]int), limits: limits}
}

func (a *pagePasswordAdmission) acquire(target string) (func(), bool) {
	a.mu.Lock()
	if a.active >= a.limits.MaxConcurrent || a.targets[target] >= a.limits.MaxConcurrentPerTarget {
		a.mu.Unlock()
		return nil, false
	}
	a.active++
	a.targets[target]++
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.active--
		a.targets[target]--
		if a.targets[target] == 0 {
			delete(a.targets, target)
		}
	}, true
}
