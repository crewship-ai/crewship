package api

import (
	"strings"
	"testing"
)

func TestRoutineFeedReadPlansUseWorkspaceOrderIndexes(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	queries := []struct {
		name   string
		sql    string
		want   string
		reject string
	}{
		{
			name: "workspace run feed",
			sql:  `SELECT id FROM pipeline_runs WHERE workspace_id = ? ORDER BY started_at DESC LIMIT 50`,
			want: "idx_pipeline_runs_workspace_started",
		},
		{
			name: "workspace schedule feed",
			sql: `SELECT id FROM pipeline_schedules WHERE workspace_id = ? AND deleted_at IS NULL
				AND NOT EXISTS (SELECT 1 FROM pipelines p WHERE p.id = pipeline_schedules.target_pipeline_id AND p.deleted_at IS NOT NULL)
 ORDER BY next_run_at ASC, id ASC LIMIT 50`,
			want:   "idx_pipeline_schedules_workspace_next_run",
			reject: "USE TEMP B-TREE",
		},
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			rows, err := db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+query.sql, "ws_plan_test")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var details []string
			for rows.Next() {
				var id, parent, notUsed int
				var detail string
				if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
					t.Fatal(err)
				}
				details = append(details, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(details, "\n")
			if query.reject != "" && strings.Contains(plan, query.reject) {
				t.Fatalf("unexpected temporary ordering: %s", plan)
			}
			if !strings.Contains(plan, query.want) {
				t.Fatalf("query plan does not use %s:\n%s", query.want, plan)
			}
		})
	}
}
