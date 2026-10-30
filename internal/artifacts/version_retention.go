package artifacts

import (
	"fmt"
	"math/bits"
	"sort"
	"strings"
	"time"
)

// Quota levels warn before history is full. Thresholds are percentages of
// limit_bytes: none < near (VersionOptions.WarningPercent, default 80) <=
// near < critical (QuotaCriticalPercent) <= critical < full (used >= limit).
// A withheld quota (shared or editor-only views, limit 0) has no level.
const (
	QuotaLevelNone       = "none"
	QuotaLevelNear       = "near"
	QuotaLevelCritical   = "critical"
	QuotaLevelFull       = "full"
	QuotaCriticalPercent = 95
)

// atLeastPercent reports used*100 >= limit*percent exactly, without int64
// overflow for very large limits.
func atLeastPercent(used, limit int64, percent int) bool {
	if used < 0 {
		used = 0
	}
	if percent < 0 {
		percent = 0
	}
	uh, ul := bits.Mul64(uint64(used), 100)
	lh, ll := bits.Mul64(uint64(limit), uint64(percent))
	return uh > lh || uh == lh && ul >= ll
}

func quotaLevel(used, limit int64, nearPercent int) string {
	if limit <= 0 {
		return ""
	}
	if used >= limit {
		return QuotaLevelFull
	}
	critical := QuotaCriticalPercent
	if nearPercent > critical {
		critical = nearPercent
	}
	switch {
	case atLeastPercent(used, limit, critical):
		return QuotaLevelCritical
	case atLeastPercent(used, limit, nearPercent):
		return QuotaLevelNear
	}
	return QuotaLevelNone
}

// RetentionPolicy is the proposed history-thinning rule. It is evaluated and
// reported only; VersionStore never executes it. Retention stays "keep-all"
// until a separately reviewed, explicitly confirmed thinning flow exists.
type RetentionPolicy struct {
	// RecentDays keeps every version created within this many days.
	RecentDays int
}

var DefaultRetentionPolicy = RetentionPolicy{RecentDays: 14}

// RetentionPreview is a dry run of RetentionPolicy over the current history.
type RetentionPreview struct {
	Policy     string `json:"policy"`
	RecentDays int    `json:"recent_days"`
	// DryRun is always true: nothing listed in WouldThin has been deleted.
	DryRun      bool    `json:"dry_run"`
	EvaluatedAt string  `json:"evaluated_at"`
	Total       int     `json:"total"`
	Retained    int     `json:"retained"`
	WouldThin   []int64 `json:"would_thin"`
}

// PlanRetention reports which versions the policy would thin out. It always
// keeps the latest and shared versions, restores and their sources, named
// versions (non-empty message), every version from the last RecentDays, the
// newest version of each older UTC day, and any version whose age is unknown.
func PlanRetention(versions []VersionInfo, latest, shared int64, now time.Time, p RetentionPolicy) RetentionPreview {
	if p.RecentDays <= 0 {
		p.RecentDays = DefaultRetentionPolicy.RecentDays
	}
	now = now.UTC()
	out := RetentionPreview{
		Policy:      fmt.Sprintf("recent-%dd-then-daily", p.RecentDays),
		RecentDays:  p.RecentDays,
		DryRun:      true,
		EvaluatedAt: now.Format(time.RFC3339),
		Total:       len(versions),
		WouldThin:   []int64{},
	}
	cutoff := now.Add(-time.Duration(p.RecentDays) * 24 * time.Hour)
	restoreSources := make(map[int64]bool)
	newestOfDay := make(map[string]int64)
	created := make(map[int64]time.Time, len(versions))
	for _, v := range versions {
		if v.RestoredFrom != 0 {
			restoreSources[v.RestoredFrom] = true
		}
		t, err := time.Parse(time.RFC3339Nano, v.CreatedAt)
		if err != nil {
			continue
		}
		created[v.Version] = t
		day := t.UTC().Format(time.DateOnly)
		if v.Version > newestOfDay[day] {
			newestOfDay[day] = v.Version
		}
	}
	for _, v := range versions {
		t, known := created[v.Version]
		keep := v.Version == latest || v.Version == shared || v.RestoredFrom != 0 || restoreSources[v.Version] ||
			strings.TrimSpace(v.Message) != "" || !known || !t.Before(cutoff) ||
			newestOfDay[t.UTC().Format(time.DateOnly)] == v.Version
		if !keep {
			out.WouldThin = append(out.WouldThin, v.Version)
		}
	}
	sort.Slice(out.WouldThin, func(i, j int) bool { return out.WouldThin[i] < out.WouldThin[j] })
	out.Retained = out.Total - len(out.WouldThin)
	return out
}
