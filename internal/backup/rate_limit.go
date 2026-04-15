package backup

import (
	"sync"
	"time"
)

// Instance-scope backup is expensive (full DB dump + credstore +
// auth keys + every workspace's container data) and rate-limiting
// the admin endpoint keeps a misbehaving script from DoSing the
// host. We copy the sliding-window pattern from internal/api/
// captain.go rather than pulling in golang.org/x/time/rate — the
// captain limiter already proves this shape is fine for the handful
// of admin calls in question.

// defaultInstanceBackupLimit is how many instance-scope backups a
// single user may create per window. One per hour is generous for
// disaster-recovery rehearsals and tight enough that a runaway
// cron hitting the endpoint does real damage only once.
const (
	defaultInstanceBackupLimit  = 1
	defaultInstanceBackupWindow = time.Hour
)

type instanceLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	recorded map[string][]time.Time
}

var instanceBackupLimiter = &instanceLimiter{
	limit:    defaultInstanceBackupLimit,
	window:   defaultInstanceBackupWindow,
	recorded: map[string][]time.Time{},
}

// AllowInstanceBackup records an attempt by userID and reports
// whether it is within the per-user sliding-window limit. Returns
// (false, retryAfter) when the limit is hit; callers map that to
// HTTP 429 with a Retry-After header. Returns (true, 0) on success.
//
// Older samples are pruned opportunistically (no background GC) so
// the map stays bounded under normal usage.
func AllowInstanceBackup(userID string) (bool, time.Duration) {
	if userID == "" {
		return true, 0
	}
	instanceBackupLimiter.mu.Lock()
	defer instanceBackupLimiter.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-instanceBackupLimiter.window)
	recorded := instanceBackupLimiter.recorded[userID]
	// Drop entries older than the window in one pass.
	fresh := recorded[:0]
	for _, t := range recorded {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= instanceBackupLimiter.limit {
		// Retry-after = time left on the OLDEST entry still in window.
		oldest := fresh[0]
		return false, instanceBackupLimiter.window - now.Sub(oldest)
	}
	fresh = append(fresh, now)
	instanceBackupLimiter.recorded[userID] = fresh
	return true, 0
}

// ResetInstanceBackupLimiter clears the per-user window cache.
// Intended for tests; production callers should not touch this.
func ResetInstanceBackupLimiter() {
	instanceBackupLimiter.mu.Lock()
	defer instanceBackupLimiter.mu.Unlock()
	instanceBackupLimiter.recorded = map[string][]time.Time{}
}
