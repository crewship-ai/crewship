// Package diskusage reports filesystem capacity for the volume backing a
// given path. Used by the admin health surface so an operator can see the
// disk climbing before it hits 100% — the failure mode that took a dev host
// down when a wedged writer filled the log volume.
package diskusage

// Stats describes the filesystem containing Path.
type Stats struct {
	Path       string  `json:"path"`
	TotalBytes uint64  `json:"total_bytes"`
	FreeBytes  uint64  `json:"free_bytes"`
	UsedBytes  uint64  `json:"used_bytes"`
	UsedPct    float64 `json:"used_pct"`
}

// Usage returns capacity stats for the filesystem that holds path, computed
// the same way `df` does so the numbers match what an operator sees:
//
//   - FreeBytes is space available to an unprivileged process (statfs Bavail
//     → df "Avail").
//   - UsedBytes counts reserved-but-not-free blocks as used (Total − Bfree →
//     df "Used"), so it doesn't understate usage on ext4-style filesystems
//     that reserve ~5% for root.
//   - UsedPct = Used / (Used + Avail) × 100 (df "Use%"), which reaches 100%
//     exactly when an unprivileged writer can no longer allocate — the
//     disk-fill condition this exists to surface.
func Usage(path string) (Stats, error) {
	total, freeAll, avail, err := rawUsage(path)
	if err != nil {
		return Stats{Path: path}, err
	}
	s := Stats{
		Path:       path,
		TotalBytes: total,
		FreeBytes:  avail,
	}
	if total >= freeAll {
		s.UsedBytes = total - freeAll
	}
	if denom := s.UsedBytes + avail; denom > 0 {
		s.UsedPct = float64(s.UsedBytes) / float64(denom) * 100
	}
	return s, nil
}
