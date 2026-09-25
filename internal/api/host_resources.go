package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

type hostResourcesHandler struct {
	db     *sql.DB
	logger *slog.Logger
	live   func() *HostResourceSample
}

const hostResourceTimeFormat = "2006-01-02T15:04:05.000000000Z"

// HostResourceSample is the latest host reading shared with the dashboard and
// Prometheus scrape path. History remains persisted separately in SQLite.
type HostResourceSample struct {
	SampledAt     string  `json:"sampled_at"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	MemoryUsedMB  int64   `json:"memory_used_mb"`
	MemoryTotalMB int64   `json:"memory_total_mb"`
}

type hostResourceBucket struct {
	TS            string   `json:"ts"`
	CPUPercent    *float64 `json:"cpu_percent"`
	MemoryPercent *float64 `json:"memory_percent"`
}

func (h hostResourcesHandler) latestSample(ctx context.Context) (*HostResourceSample, error) {
	if h.live != nil {
		if current := h.live(); current != nil {
			return current, nil
		}
	}
	var latest HostResourceSample
	err := h.db.QueryRowContext(ctx,
		`SELECT ts, cpu_percent, memory_percent, memory_used_mb, memory_total_mb FROM host_resource_samples ORDER BY ts DESC LIMIT 1`,
	).Scan(&latest.SampledAt, &latest.CPUPercent, &latest.MemoryPercent, &latest.MemoryUsedMB, &latest.MemoryTotalMB)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &latest, nil
}

// Latest serves the in-memory reading without scanning chart history. The
// dashboard polls this inexpensive route while System details is open.
func (h hostResourcesHandler) Latest(w http.ResponseWriter, r *http.Request) {
	latest, err := h.latestSample(r.Context())
	if err != nil {
		replyInternalError(w, h.logger, "host resources: latest", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Latest *HostResourceSample `json:"latest"`
	}{Latest: latest})
}

// Resources reports real, persisted host CPU and RAM measurements. Empty
// history is explicit: collection starts when this version first runs, not
// retroactively 30 days before installation.
func (h hostResourcesHandler) Resources(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	var duration time.Duration
	var bucketSeconds int64
	switch window {
	case "24h":
		duration, bucketSeconds = 24*time.Hour, 5*60
	case "7d":
		duration, bucketSeconds = 7*24*time.Hour, 60*60
	case "30d":
		duration, bucketSeconds = 30*24*time.Hour, 6*60*60
	default:
		replyError(w, http.StatusBadRequest, "window must be 24h, 7d or 30d")
		return
	}

	var latest HostResourceSample
	err := h.db.QueryRowContext(r.Context(),
		`SELECT ts, cpu_percent, memory_percent, memory_used_mb, memory_total_mb FROM host_resource_samples ORDER BY ts DESC LIMIT 1`,
	).Scan(&latest.SampledAt, &latest.CPUPercent, &latest.MemoryPercent, &latest.MemoryUsedMB, &latest.MemoryTotalMB)
	if err != nil && err != sql.ErrNoRows {
		replyInternalError(w, h.logger, "host resources: latest", err)
		return
	}
	var latestPtr *HostResourceSample
	if err == nil {
		latestPtr = &latest
	}
	if h.live != nil {
		if current := h.live(); current != nil && (latestPtr == nil || current.SampledAt > latestPtr.SampledAt) {
			latestPtr = current
		}
	}

	var recordingSince sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT MIN(ts) FROM host_resource_samples`).Scan(&recordingSince); err != nil {
		replyInternalError(w, h.logger, "host resources: first sample", err)
		return
	}

	now := time.Now().UTC()
	since := now.Add(-duration)
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT CAST(strftime('%s', ts) AS INTEGER) / ? AS slot,
		       AVG(cpu_percent), AVG(memory_percent)
		FROM host_resource_samples WHERE ts >= ?
		GROUP BY slot ORDER BY slot`, bucketSeconds, since.Format(hostResourceTimeFormat))
	if err != nil {
		replyInternalError(w, h.logger, "host resources: history", err)
		return
	}
	defer rows.Close()
	observed := make(map[int64]hostResourceBucket)
	for rows.Next() {
		var slot int64
		var cpu, memory float64
		if err := rows.Scan(&slot, &cpu, &memory); err != nil {
			replyInternalError(w, h.logger, "host resources: scan history", err)
			return
		}
		observed[slot] = hostResourceBucket{CPUPercent: &cpu, MemoryPercent: &memory}
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "host resources: read history", err)
		return
	}
	first := since.Unix() / bucketSeconds
	last := now.Unix() / bucketSeconds
	series := make([]hostResourceBucket, 0, last-first+1)
	for slot := first; slot <= last; slot++ {
		bucket := observed[slot]
		bucket.TS = time.Unix(slot*bucketSeconds, 0).UTC().Format(time.RFC3339)
		series = append(series, bucket)
	}
	var started *string
	if recordingSince.Valid {
		started = &recordingSince.String
	} else if latestPtr != nil {
		started = &latestPtr.SampledAt
	}
	writeJSON(w, http.StatusOK, struct {
		Window         string               `json:"window"`
		RecordingSince *string              `json:"recording_since"`
		Latest         *HostResourceSample  `json:"latest"`
		Series         []hostResourceBucket `json:"series"`
	}{Window: window, RecordingSince: started, Latest: latestPtr, Series: series})
}
