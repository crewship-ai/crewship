package config

import (
	"fmt"
	"runtime"
)

// PublicPagePasswordsConfig bounds concurrent password work for public Pages
// in one server process. Zero selects CPU-based defaults; it never disables
// admission. Configuration changes require a restart.
type PublicPagePasswordsConfig struct {
	MaxConcurrent          int `yaml:"max_concurrent"`
	MaxConcurrentPerTarget int `yaml:"max_concurrent_per_target"`
}

// Resolve derives the default from the Go runtime's CPU concurrency budget.
// This allows approximately half that budget for page-password work, at least
// one and at most
// four checks. It is a capacity policy, not a benchmark or a hard CPU quota.
func (c PublicPagePasswordsConfig) Resolve() (PublicPagePasswordsConfig, error) {
	if c.MaxConcurrent < 0 || c.MaxConcurrent > 1024 || c.MaxConcurrentPerTarget < 0 || c.MaxConcurrentPerTarget > 1024 {
		return c, fmt.Errorf("auth.public_page_passwords limits must be between 0 (automatic) and 1024")
	}
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = defaultPublicPagePasswordConcurrency(runtime.GOMAXPROCS(0))
	}
	if c.MaxConcurrentPerTarget == 0 {
		c.MaxConcurrentPerTarget = 1
	}
	if c.MaxConcurrentPerTarget > c.MaxConcurrent {
		return c, fmt.Errorf("auth.public_page_passwords.max_concurrent_per_target cannot exceed max_concurrent")
	}
	return c, nil
}

func defaultPublicPagePasswordConcurrency(cpus int) int { return max(1, min(4, cpus/2)) }
