package api

import (
	"database/sql"
	"net/http"
	"time"
)

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
