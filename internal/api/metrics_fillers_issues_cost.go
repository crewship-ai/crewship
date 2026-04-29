package api

import (
	"net/http"
)

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
			  AND m.status IN ('DONE','COMPLETED','REVIEW')
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
			  AND m.status IN ('DONE','COMPLETED','REVIEW')
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
			  AND m.status IN ('DONE','COMPLETED','REVIEW')
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
