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

// Usage returns capacity stats for the filesystem that holds path. FreeBytes
// is the space available to an unprivileged process (matching what `df`
// reports as "Avail"), so UsedPct lines up with the number an operator sees.
func Usage(path string) (Stats, error) {
	total, free, err := rawUsage(path)
	if err != nil {
		return Stats{Path: path}, err
	}
	s := Stats{
		Path:       path,
		TotalBytes: total,
		FreeBytes:  free,
	}
	if total >= free {
		s.UsedBytes = total - free
	}
	if total > 0 {
		s.UsedPct = float64(s.UsedBytes) / float64(total) * 100
	}
	return s, nil
}
