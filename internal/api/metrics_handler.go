package api

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// MetricsHandler serves bucketed time-series metrics for dashboard charts.
//
// It reads `workspace_id` from the request context (injected by wsCtx middleware)
// and exposes a single endpoint, GET /api/v1/metrics/timeseries, which returns
// zero-filled bucket sequences so the client never has to patch visual gaps.
type MetricsHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewMetricsHandler constructs a MetricsHandler.
func NewMetricsHandler(db *sql.DB, logger *slog.Logger) *MetricsHandler {
	return &MetricsHandler{db: db, logger: logger}
}

// Allowed parameter values.
var (
	validMetrics = map[string]struct{}{
		"issues_closed":   {},
		"cost_usd":        {},
		"runs_count":      {},
		"active_missions": {},
	}
	validWindows = map[string]time.Duration{
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
	}
	validBuckets = map[string]time.Duration{
		"15m": 15 * time.Minute,
		"1h":  1 * time.Hour,
		"1d":  24 * time.Hour,
	}
	validGroupBy = map[string]struct{}{
		"crew":   {},
		"model":  {},
		"status": {},
		"none":   {},
	}
)

// metricsResponse is the full response body for the timeseries endpoint.
// Series values are always floats on the wire so cost_usd and integer counts
// share a single JSON shape and the frontend never has to branch on type.
type metricsResponse struct {
	Metric       string             `json:"metric"`
	Window       string             `json:"window"`
	Bucket       string             `json:"bucket"`
	GroupBy      string             `json:"group_by"`
	Buckets      []metricsBucket    `json:"buckets"`
	SeriesLabels map[string]string  `json:"series_labels"`
}

// metricsBucket is one row of the time series: a bucket-start timestamp and a
// map of series key to numeric value.
type metricsBucket struct {
	TS     string             `json:"ts"`
	Series map[string]float64 `json:"series"`
}

// timeseriesParams holds validated query parameters.
type timeseriesParams struct {
	Metric      string
	Window      string
	Bucket      string
	GroupBy     string
	WindowDur   time.Duration
	BucketDur   time.Duration
	WorkspaceID string
	Now         time.Time
}

// parseTimeseriesParams validates query parameters and returns a populated
// timeseriesParams, or an HTTP status + user-facing error message on failure.
func parseTimeseriesParams(r *http.Request) (timeseriesParams, int, string) {
	q := r.URL.Query()
	p := timeseriesParams{
		Metric:      q.Get("metric"),
		Window:      q.Get("window"),
		Bucket:      q.Get("bucket"),
		GroupBy:     q.Get("group_by"),
		WorkspaceID: WorkspaceIDFromContext(r.Context()),
		Now:         time.Now().UTC(),
	}
	if p.Window == "" {
		p.Window = "24h"
	}
	if p.Bucket == "" {
		p.Bucket = "1h"
	}
	if p.GroupBy == "" {
		p.GroupBy = "none"
	}

	if _, ok := validMetrics[p.Metric]; !ok {
		return p, http.StatusBadRequest, "invalid metric; must be one of issues_closed, cost_usd, runs_count, active_missions"
	}
	wdur, ok := validWindows[p.Window]
	if !ok {
		return p, http.StatusBadRequest, "invalid window; must be one of 24h, 7d, 30d"
	}
	p.WindowDur = wdur

	bdur, ok := validBuckets[p.Bucket]
	if !ok {
		return p, http.StatusBadRequest, "invalid bucket; must be one of 15m, 1h, 1d"
	}
	p.BucketDur = bdur

	if _, ok := validGroupBy[p.GroupBy]; !ok {
		return p, http.StatusBadRequest, "invalid group_by; must be one of crew, model, status, none"
	}

	// Combination rules: at least two buckets, cap total buckets to keep the
	// response small and the DB work bounded.
	if p.BucketDur >= p.WindowDur {
		return p, http.StatusBadRequest, fmt.Sprintf("bucket %s is too large for window %s", p.Bucket, p.Window)
	}
	nBuckets := int(p.WindowDur / p.BucketDur)
	if nBuckets > 200 {
		return p, http.StatusBadRequest, fmt.Sprintf("bucket %s against window %s would produce %d buckets (max 200)", p.Bucket, p.Window, nBuckets)
	}

	// group_by semantics per metric.
	switch p.GroupBy {
	case "model":
		if p.Metric != "cost_usd" {
			return p, http.StatusBadRequest, "group_by=model is only valid for metric=cost_usd"
		}
	case "crew":
		if p.Metric != "issues_closed" && p.Metric != "runs_count" {
			return p, http.StatusBadRequest, "group_by=crew is only valid for metric=issues_closed or runs_count"
		}
	case "status":
		if p.Metric != "active_missions" && p.Metric != "issues_closed" {
			return p, http.StatusBadRequest, "group_by=status is only valid for metric=issues_closed or active_missions"
		}
	}

	if p.WorkspaceID == "" {
		return p, http.StatusUnauthorized, "workspace context missing"
	}
	return p, 0, ""
}

// bucketStartsFor returns the ordered list of bucket-start timestamps for the
// window, aligned to UTC wall-clock boundaries matching the sqlite strftime
// expressions used in queries. The last bucket starts <= now and the first is
// (now - window) truncated to a bucket boundary.
func bucketStartsFor(p timeseriesParams) []time.Time {
	end := truncateToBucket(p.Now, p.BucketDur)
	start := end.Add(-p.WindowDur + p.BucketDur) // inclusive window of length WindowDur
	n := int(p.WindowDur / p.BucketDur)
	out := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, start.Add(time.Duration(i)*p.BucketDur))
	}
	return out
}

// truncateToBucket rounds t down to the nearest bucket boundary in UTC.
// For 15m this is 00/15/30/45, for 1h the top of the hour, for 1d UTC midnight.
func truncateToBucket(t time.Time, bucket time.Duration) time.Time {
	t = t.UTC()
	switch bucket {
	case 15 * time.Minute:
		min := (t.Minute() / 15) * 15
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), min, 0, 0, time.UTC)
	case time.Hour:
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC)
	case 24 * time.Hour:
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return t
}

// bucketKey returns the canonical ISO-8601 string for a bucket start time,
// matching the format used by the sqlite strftime expressions so post-query
// lookups key-match on equal strings.
func bucketKey(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// sqliteBucketExpr returns a SQLite SQL expression that maps a timestamp
// column to its bucket-start ISO-8601 string. The expressions match
// truncateToBucket / bucketKey exactly.
func sqliteBucketExpr(col string, bucket time.Duration) string {
	switch bucket {
	case 15 * time.Minute:
		return "strftime('%Y-%m-%dT%H:', " + col + ") || printf('%02d', (cast(strftime('%M'," + col + ") as int)/15)*15) || ':00Z'"
	case time.Hour:
		return "strftime('%Y-%m-%dT%H:00:00Z', " + col + ")"
	case 24 * time.Hour:
		return "strftime('%Y-%m-%dT00:00:00Z', " + col + ")"
	}
	return "strftime('%Y-%m-%dT%H:00:00Z', " + col + ")"
}

// Timeseries handles GET /api/v1/metrics/timeseries.
func (h *MetricsHandler) Timeseries(w http.ResponseWriter, r *http.Request) {
	p, code, msg := parseTimeseriesParams(r)
	if code != 0 {
		writeProblem(w, r, code, msg)
		return
	}

	starts := bucketStartsFor(p)
	// Precompute lookup by key with a stable zero-filled initial row per bucket.
	resp := metricsResponse{
		Metric:       p.Metric,
		Window:       p.Window,
		Bucket:       p.Bucket,
		GroupBy:      p.GroupBy,
		Buckets:      make([]metricsBucket, len(starts)),
		SeriesLabels: map[string]string{},
	}
	idxByKey := make(map[string]int, len(starts))
	for i, s := range starts {
		k := bucketKey(s)
		resp.Buckets[i] = metricsBucket{TS: k, Series: map[string]float64{}}
		idxByKey[k] = i
	}

	var err error
	switch p.Metric {
	case "issues_closed":
		err = h.fillIssuesClosed(r, p, idxByKey, &resp)
	case "cost_usd":
		err = h.fillCostUSD(r, p, idxByKey, &resp)
	case "runs_count":
		err = h.fillRunsCount(r, p, idxByKey, &resp)
	case "active_missions":
		err = h.fillActiveMissions(r, p, starts, idxByKey, &resp)
	}
	if err != nil {
		h.logger.Error("metrics timeseries query failed",
			"metric", p.Metric,
			"window", p.Window,
			"bucket", p.Bucket,
			"group_by", p.GroupBy,
			"workspace_id", p.WorkspaceID,
			"error", err,
		)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// For group_by=none, ensure every bucket has the "total" key (even if zero).
	if p.GroupBy == "none" {
		if _, has := resp.SeriesLabels["total"]; !has {
			resp.SeriesLabels["total"] = "Total"
		}
		for i := range resp.Buckets {
			if _, ok := resp.Buckets[i].Series["total"]; !ok {
				resp.Buckets[i].Series["total"] = 0
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// windowStartCutoff returns the RFC3339 string for the earliest time still
// inside the reported window, so queries can filter with `>= cutoff`.
// The cutoff is aligned with bucket boundaries so that the first bucket is
// fully populated rather than truncated.
func windowStartCutoff(p timeseriesParams) string {
	end := truncateToBucket(p.Now, p.BucketDur)
	start := end.Add(-p.WindowDur + p.BucketDur)
	return start.UTC().Format(time.RFC3339)
}

// fillIssuesClosed counts missions of type 'issue' that closed in the window,
// grouped by bucket and optionally by crew or status.
func (h *MetricsHandler) fillIssuesClosed(r *http.Request, p timeseriesParams, idxByKey map[string]int, resp *metricsResponse) error {
	cutoff := windowStartCutoff(p)
	bucketExpr := sqliteBucketExpr("m.completed_at", p.BucketDur)

	type row struct {
		ts    string
		key   string
		label string
		count int64
	}
	var rows []row

	ctx := r.Context()
	switch p.GroupBy {
	case "crew":
		q := `SELECT ` + bucketExpr + ` AS ts, m.crew_id, COALESCE(c.name, m.crew_id), COUNT(*)
			FROM missions m
			LEFT JOIN crews c ON c.id = m.crew_id
			WHERE m.workspace_id = ?
			  AND COALESCE(m.mission_type, 'orchestration') = 'issue'
			  AND m.completed_at IS NOT NULL
			  AND m.completed_at >= ?
			  AND m.status IN ('DONE','COMPLETED')
			GROUP BY ts, m.crew_id
			ORDER BY ts`
		rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, cutoff)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.ts, &r.key, &r.label, &r.count); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	case "status":
		q := `SELECT ` + bucketExpr + ` AS ts, m.status, m.status, COUNT(*)
			FROM missions m
			WHERE m.workspace_id = ?
			  AND COALESCE(m.mission_type, 'orchestration') = 'issue'
			  AND m.completed_at IS NOT NULL
			  AND m.completed_at >= ?
			  AND m.status IN ('DONE','COMPLETED')
			GROUP BY ts, m.status
			ORDER BY ts`
		rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, cutoff)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.ts, &r.key, &r.label, &r.count); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	default: // none
		q := `SELECT ` + bucketExpr + ` AS ts, COUNT(*)
			FROM missions m
			WHERE m.workspace_id = ?
			  AND COALESCE(m.mission_type, 'orchestration') = 'issue'
			  AND m.completed_at IS NOT NULL
			  AND m.completed_at >= ?
			  AND m.status IN ('DONE','COMPLETED')
			GROUP BY ts
			ORDER BY ts`
		rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, cutoff)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			r.key = "total"
			r.label = "Total"
			if err := rs.Scan(&r.ts, &r.count); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	}

	for _, row := range rows {
		idx, ok := idxByKey[row.ts]
		if !ok {
			continue // silently drop rows outside our bucket grid (e.g. clock skew)
		}
		resp.Buckets[idx].Series[row.key] += float64(row.count)
		if _, exists := resp.SeriesLabels[row.key]; !exists {
			resp.SeriesLabels[row.key] = row.label
		}
	}
	return nil
}

// fillCostUSD sums mission cost in the window, bucketed by updated_at. For
// group_by=model the per-mission lead agent's llm_model is used as a proxy
// (mission_tasks does not carry per-task cost or model in this schema).
func (h *MetricsHandler) fillCostUSD(r *http.Request, p timeseriesParams, idxByKey map[string]int, resp *metricsResponse) error {
	cutoff := windowStartCutoff(p)
	bucketExpr := sqliteBucketExpr("m.updated_at", p.BucketDur)
	ctx := r.Context()

	type row struct {
		ts    string
		key   string
		label string
		cost  float64
	}
	var rows []row

	switch p.GroupBy {
	case "model":
		q := `SELECT ` + bucketExpr + ` AS ts,
			       COALESCE(a.llm_model, 'unknown') AS model_key,
			       COALESCE(a.llm_model, 'unknown') AS model_label,
			       COALESCE(SUM(COALESCE(m.total_estimated_cost, 0)), 0)
			FROM missions m
			LEFT JOIN agents a ON a.id = m.lead_agent_id
			WHERE m.workspace_id = ?
			  AND m.updated_at >= ?
			GROUP BY ts, model_key
			ORDER BY ts`
		rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, cutoff)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.ts, &r.key, &r.label, &r.cost); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	default: // none
		q := `SELECT ` + bucketExpr + ` AS ts,
			       COALESCE(SUM(COALESCE(m.total_estimated_cost, 0)), 0)
			FROM missions m
			WHERE m.workspace_id = ?
			  AND m.updated_at >= ?
			GROUP BY ts
			ORDER BY ts`
		rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, cutoff)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			r.key = "total"
			r.label = "Total"
			if err := rs.Scan(&r.ts, &r.cost); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	}

	for _, row := range rows {
		idx, ok := idxByKey[row.ts]
		if !ok {
			continue
		}
		resp.Buckets[idx].Series[row.key] += row.cost
		if _, exists := resp.SeriesLabels[row.key]; !exists {
			resp.SeriesLabels[row.key] = row.label
		}
	}
	return nil
}

// fillRunsCount counts agent_runs created in the window, bucketed by
// created_at, optionally grouped by the running agent's crew.
func (h *MetricsHandler) fillRunsCount(r *http.Request, p timeseriesParams, idxByKey map[string]int, resp *metricsResponse) error {
	cutoff := windowStartCutoff(p)
	bucketExpr := sqliteBucketExpr("ar.created_at", p.BucketDur)
	ctx := r.Context()

	type row struct {
		ts    string
		key   string
		label string
		count int64
	}
	var rows []row

	switch p.GroupBy {
	case "crew":
		q := `SELECT ` + bucketExpr + ` AS ts,
			       COALESCE(a.crew_id, 'unassigned') AS crew_key,
			       COALESCE(c.name, 'Unassigned') AS crew_label,
			       COUNT(*)
			FROM agent_runs ar
			LEFT JOIN agents a ON a.id = ar.agent_id
			LEFT JOIN crews c ON c.id = a.crew_id
			WHERE ar.workspace_id = ?
			  AND ar.created_at >= ?
			GROUP BY ts, crew_key
			ORDER BY ts`
		rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, cutoff)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.ts, &r.key, &r.label, &r.count); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	default: // none
		q := `SELECT ` + bucketExpr + ` AS ts, COUNT(*)
			FROM agent_runs ar
			WHERE ar.workspace_id = ?
			  AND ar.created_at >= ?
			GROUP BY ts
			ORDER BY ts`
		rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, cutoff)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			r.key = "total"
			r.label = "Total"
			if err := rs.Scan(&r.ts, &r.count); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		if err := rs.Err(); err != nil {
			return err
		}
	}

	for _, row := range rows {
		idx, ok := idxByKey[row.ts]
		if !ok {
			continue
		}
		resp.Buckets[idx].Series[row.key] += float64(row.count)
		if _, exists := resp.SeriesLabels[row.key]; !exists {
			resp.SeriesLabels[row.key] = row.label
		}
	}
	return nil
}

// fillActiveMissions computes an at-a-point-in-time count of missions in
// IN_PROGRESS or REVIEW state at each bucket boundary (bucket_end). Unlike the
// event-based metrics, this is computed in Go after a single SELECT of all
// potentially-relevant missions, to avoid running O(buckets) SQL queries.
func (h *MetricsHandler) fillActiveMissions(r *http.Request, p timeseriesParams, starts []time.Time, idxByKey map[string]int, resp *metricsResponse) error {
	// A mission counts as active at time T if:
	//   created_at <= T AND (completed_at IS NULL OR completed_at > T)
	//   AND status IN (...) at T.
	// We only have the current status stored, so we use the following heuristic:
	// a mission that is currently in an active state contributes while its
	// created_at..completed_at/now range intersects the bucket point; a mission
	// that is currently completed/failed/cancelled contributes for T between
	// created_at and completed_at regardless of final state, since those
	// missions were in-progress at some point. This matches what dashboards
	// typically want ("how many were in-flight then") without a separate event
	// log.
	ctx := r.Context()

	// Only need missions whose lifespan overlaps [firstBucket, lastBucket].
	if len(starts) == 0 {
		return nil
	}
	firstEnd := starts[0].Add(p.BucketDur).UTC().Format(time.RFC3339)

	q := `SELECT m.created_at, m.completed_at, m.status
		FROM missions m
		WHERE m.workspace_id = ?
		  AND (m.completed_at IS NULL OR m.completed_at >= ?)`
	rs, err := h.db.QueryContext(ctx, q, p.WorkspaceID, firstEnd)
	if err != nil {
		return err
	}
	defer rs.Close()

	type mrec struct {
		created   time.Time
		completed time.Time
		hasEnd    bool
		status    string
	}
	var recs []mrec
	for rs.Next() {
		var createdStr string
		var completedStr sql.NullString
		var status string
		if err := rs.Scan(&createdStr, &completedStr, &status); err != nil {
			return err
		}
		created, err := parseDBTime(createdStr)
		if err != nil {
			continue
		}
		rec := mrec{created: created, status: status}
		if completedStr.Valid && completedStr.String != "" {
			if t, err := parseDBTime(completedStr.String); err == nil {
				rec.completed = t
				rec.hasEnd = true
			}
		}
		recs = append(recs, rec)
	}
	if err := rs.Err(); err != nil {
		return err
	}

	for i, s := range starts {
		bucketEnd := s.Add(p.BucketDur)
		var totalActive int64
		statusCounts := map[string]int64{}
		for _, rec := range recs {
			if !rec.created.Before(bucketEnd) && !rec.created.Equal(bucketEnd) {
				continue // created after bucket end
			}
			if rec.hasEnd && !rec.completed.After(bucketEnd) {
				continue // ended at or before bucket end
			}
			// Mission was alive at bucketEnd. Filter on whether it was in an
			// "active" state: if it eventually completed it was in progress
			// during its lifespan, so count it. If current status is one of
			// the active states we also count it. This is deliberately
			// permissive for historical buckets.
			totalActive++
			statusCounts[rec.status]++
		}
		key := bucketKey(s)
		idx := idxByKey[key]
		switch p.GroupBy {
		case "status":
			for status, count := range statusCounts {
				resp.Buckets[idx].Series[status] = float64(count)
				if _, exists := resp.SeriesLabels[status]; !exists {
					resp.SeriesLabels[status] = status
				}
			}
		default: // none
			resp.Buckets[i].Series["total"] = float64(totalActive)
			resp.SeriesLabels["total"] = "Total"
		}
	}
	return nil
}

// parseDBTime parses a timestamp string as written by SQLite defaults or the
// RFC3339 format used by the Go side. Returns an error if neither works.
func parseDBTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty time string")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp: %q", s)
}
