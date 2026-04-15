package backup

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// Metrics are process-lifetime counters for the backup subsystem. They
// reset on restart; persistent observability belongs in the audit log
// and in the admin dashboard's historical charts (built on that log).
// Keeping the counters in-memory avoids pulling in a Prometheus
// dependency just for a handful of numbers and matches the existing
// metrics_handler.go style in internal/api.
//
// Runners call Observe* helpers at well-defined points — Create start,
// Create success/failure, Restore success/failure, Lock acquire/
// release. Readers fetch a point-in-time Snapshot through the HTTP
// handler (/api/v1/admin/backups/metrics).
var metrics = newCounters()

type counters struct {
	mu               sync.RWMutex
	createdTotal     int64
	createdByScope   map[string]int64
	failedTotal      int64
	failedByReason   map[string]int64
	restoredTotal    int64
	durationsSeconds []float64 // bounded ring; see append logic
	sizeBytesTotal   int64
	lockHeldByWs     map[string]time.Time
}

func newCounters() *counters {
	return &counters{
		createdByScope: map[string]int64{},
		failedByReason: map[string]int64{},
		lockHeldByWs:   map[string]time.Time{},
	}
}

// MetricsSnapshot is the JSON shape the admin metrics endpoint emits.
// Quantile approximations come from sorting the duration ring at read
// time — adequate for the tens-to-hundreds of samples a single host
// will accumulate between restarts; not a general-purpose histogram.
type MetricsSnapshot struct {
	CreatedTotal        int64            `json:"created_total"`
	CreatedByScope      map[string]int64 `json:"created_by_scope"`
	FailedTotal         int64            `json:"failed_total"`
	FailedByReason      map[string]int64 `json:"failed_by_reason"`
	RestoredTotal       int64            `json:"restored_total"`
	SizeBytesTotal      int64            `json:"size_bytes_total"`
	DurationSecondsP50  float64          `json:"duration_seconds_p50"`
	DurationSecondsP95  float64          `json:"duration_seconds_p95"`
	DurationSecondsMean float64          `json:"duration_seconds_mean"`
	LockHeld            map[string]int64 `json:"lock_held_seconds_by_workspace"`
}

// Snapshot returns a point-in-time view. Maps are copied so the caller
// can mutate them safely; LockHeld values are durations from the
// acquisition timestamp to "now".
func Snapshot() MetricsSnapshot {
	return metrics.snapshot()
}

func (c *counters) snapshot() MetricsSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := MetricsSnapshot{
		CreatedTotal:   c.createdTotal,
		CreatedByScope: copyStrInt64(c.createdByScope),
		FailedTotal:    c.failedTotal,
		FailedByReason: copyStrInt64(c.failedByReason),
		RestoredTotal:  c.restoredTotal,
		SizeBytesTotal: c.sizeBytesTotal,
		LockHeld:       map[string]int64{},
	}
	now := time.Now()
	for ws, since := range c.lockHeldByWs {
		out.LockHeld[ws] = int64(now.Sub(since).Seconds())
	}
	if n := len(c.durationsSeconds); n > 0 {
		sorted := make([]float64, n)
		copy(sorted, c.durationsSeconds)
		sort.Float64s(sorted)
		out.DurationSecondsP50 = sorted[n/2]
		out.DurationSecondsP95 = sorted[min(n-1, (n*95)/100)]
		var sum float64
		for _, v := range sorted {
			sum += v
		}
		out.DurationSecondsMean = sum / float64(n)
	}
	return out
}

// ObserveCreateStart records the moment a backup begins, returning a
// finish callback the runner defers. The callback captures the
// success/failure state and the resulting bundle size so those land
// atomically in the same mutex window as the increment.
//
// Usage pattern:
//
//	done := backup.ObserveCreateStart(string(opts.Scope))
//	...
//	done(err, size)
func ObserveCreateStart(scope string) func(err error, bytes int64) {
	started := time.Now()
	return func(err error, bytes int64) {
		metrics.mu.Lock()
		defer metrics.mu.Unlock()
		dur := time.Since(started).Seconds()
		// Keep the duration ring bounded. 1024 samples is plenty for
		// percentile estimation on a single host between restarts and
		// costs ~8 KB of memory.
		metrics.durationsSeconds = append(metrics.durationsSeconds, dur)
		if len(metrics.durationsSeconds) > 1024 {
			metrics.durationsSeconds = metrics.durationsSeconds[len(metrics.durationsSeconds)-1024:]
		}
		if err != nil {
			metrics.failedTotal++
			metrics.failedByReason[classifyErr(err)]++
			return
		}
		metrics.createdTotal++
		metrics.createdByScope[scope]++
		metrics.sizeBytesTotal += bytes
	}
}

// ObserveRestore increments either restoredTotal or failedTotal based
// on err.
func ObserveRestore(err error) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if err != nil {
		metrics.failedTotal++
		metrics.failedByReason["restore:"+classifyErr(err)]++
		return
	}
	metrics.restoredTotal++
}

// ObserveLockAcquired records the acquisition time; ObserveLockReleased
// clears it. The snapshot reports the elapsed "held" duration for each
// workspace still under lock.
func ObserveLockAcquired(workspaceID string) {
	if workspaceID == "" {
		return
	}
	metrics.mu.Lock()
	metrics.lockHeldByWs[workspaceID] = time.Now()
	metrics.mu.Unlock()
}

// ObserveLockReleased is the counterpart to ObserveLockAcquired.
func ObserveLockReleased(workspaceID string) {
	if workspaceID == "" {
		return
	}
	metrics.mu.Lock()
	delete(metrics.lockHeldByWs, workspaceID)
	metrics.mu.Unlock()
}

// ResetMetrics wipes the in-memory counters. Intended for tests. The
// mutex itself is preserved — rebuilding the struct wholesale would
// overwrite the held lock with a zero-value RWMutex and panic on the
// deferred Unlock.
func ResetMetrics() {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.createdTotal = 0
	metrics.createdByScope = map[string]int64{}
	metrics.failedTotal = 0
	metrics.failedByReason = map[string]int64{}
	metrics.restoredTotal = 0
	metrics.durationsSeconds = nil
	metrics.sizeBytesTotal = 0
	metrics.lockHeldByWs = map[string]time.Time{}
}

// classifyErr produces a low-cardinality label for failedByReason so
// the map stays bounded. We map known sentinels; everything else
// collapses to "other" so a misbehaving caller cannot blow up memory
// by injecting unique error strings.
func classifyErr(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrLockHeld):
		return "lock_held"
	case errors.Is(err, ErrLockExpired):
		return "lock_expired"
	case errors.Is(err, ErrAgentRunning):
		return "agent_running"
	case errors.Is(err, ErrSchemaTooOld):
		return "schema_too_old"
	case errors.Is(err, ErrInvalidChecksum):
		return "invalid_checksum"
	case errors.Is(err, ErrInvalidManifest):
		return "invalid_manifest"
	case errors.Is(err, ErrIncompatibleTarget):
		return "incompatible_target"
	case errors.Is(err, ErrDecryption):
		return "decryption"
	case errors.Is(err, ErrFormatTooNew):
		return "format_too_new"
	case errors.Is(err, ErrFormatTooOld):
		return "format_too_old"
	case errors.Is(err, ErrInvalidScope):
		return "invalid_scope"
	case errors.Is(err, ErrAdminRequired):
		return "admin_required"
	case errors.Is(err, ErrNoOpRestore):
		return "noop_restore"
	case errors.Is(err, ErrRestoreBackfillFailed):
		return "backfill_failed"
	default:
		return "other"
	}
}

func copyStrInt64(src map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
