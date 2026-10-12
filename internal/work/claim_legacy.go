package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// During assignment cutover only pending work transfers ownership. Already
// running legacy assignments retain their executor and must still occupy every
// applicable slot. Deduplicate by domain identity, never by agent: two different
// assignments on one agent remain two executions. A queued work row cannot
// erase a running legacy hold.
var liveSlotsCTE = `WITH live_slots AS (
 SELECT workspace_id,agent_id,crew_id,session_id,class
 FROM work_items WHERE state IN (` + sqlHoldsExecutionSlot + `)
 UNION ALL
 SELECT a.workspace_id,a.assigned_to_id,COALESCE(ag.crew_id,''),COALESCE(a.session_id,''),'background'
 FROM assignments a JOIN agents ag ON ag.id=a.assigned_to_id AND ag.workspace_id=a.workspace_id
 WHERE a.status='RUNNING' AND NOT EXISTS (
  SELECT 1 FROM work_items w WHERE w.workspace_id=a.workspace_id
  AND w.source='assignment' AND w.domain_kind='assignment' AND w.domain_id=a.id
  AND w.state IN (` + sqlHoldsExecutionSlot + `))
) `

// Match the existing crew budget contract inside the same admission transaction.
// Only validated integers enter the SQL expression; crewID is a fixed SQL
// expression supplied by these claim methods, never caller text.
func crewBudgetExpression(ctx context.Context, tx *sql.Tx, crewID string) (string, error) {
	minMB := 2048
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key='runtime.agent_min_memory_mb'`).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("work: read crew memory floor: %w", err)
	}
	if n, parseErr := strconv.Atoi(raw); parseErr == nil && n >= 6 && n <= 262144 {
		minMB = n
	}
	return `(COALESCE((SELECT CASE
 WHEN max_concurrent_agents IS NOT NULL THEN
  CASE WHEN typeof(max_concurrent_agents)='integer' AND max_concurrent_agents>0 THEN max_concurrent_agents ELSE 0 END
 WHEN container_memory_mb IS NULL THEN 1
 WHEN typeof(container_memory_mb)<>'integer' THEN 0
 ELSE MAX(1,container_memory_mb/` + strconv.Itoa(minMB) + `) END
 FROM crews WHERE id=` + crewID + `),1))`, nil
}
