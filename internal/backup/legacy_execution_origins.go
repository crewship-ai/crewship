package backup

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// fenceLegacyRestoredWork covers imports made AFTER the startup cutover.
// An absent column means pre-provenance data; explicit NULL in a current dump
// is genuine autonomous work and is never rewritten. Keep records and status,
// adding only an immutable invalid authority fence requiring fresh admission.
func fenceLegacyRestoredWork(ctx context.Context, tx *sql.Tx, table string, row map[string]any, allowed map[string]bool, cache map[string]string) (map[string]any, error) {
	if _, present := row["chat_origin_id"]; present {
		return row, nil
	}
	status, _ := row["status"].(string)
	candidate := false
	switch table {
	case "assignments":
		candidate = status == "" || status == "PENDING" || status == "QUEUED" || status == "RUNNING"
	case "missions":
		kind, _ := row["mission_type"].(string)
		candidate = (kind == "" || kind == "orchestration") && status != "COMPLETED" && status != "FAILED" && status != "CANCELLED"
	case "pending_runs":
		candidate = status == "" || status == "pending" || status == "fired"
	case "pipeline_runs":
		candidate = status == "queued" || status == "running" || status == "waiting" || status == "interrupted"
	}
	if !candidate {
		return row, nil
	}
	workspace, _ := row["workspace_id"].(string)
	if table == "pending_runs" || table == "pipeline_runs" {
		// Existing standing machine grants remain valid. Only durable evidence of
		// a human parent, bound to this workspace, fences legacy routine work.
		var human bool
		if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=? AND j.entry_type='run.started' AND j.trace_id IN(?,?)
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.human_authority.chat_id'),'') ELSE '' END<>''
 AND CASE WHEN json_valid(j.payload) THEN COALESCE(json_extract(j.payload,'$.trigger_message_id'),'') ELSE '' END<>'')`, workspace, row["chain_origin"], row["triggered_by_id"]).Scan(&human); e != nil {
			return nil, e
		}
		if !human {
			return row, nil
		}
	}
	if workspace == "" || !allowed["chat_origin_id"] {
		return nil, fmt.Errorf("backup: unsupported authority schema: cannot fence legacy %s", table)
	}
	id := cache[workspace]
	if id == "" {
		id = newRemapCUID()
		if _, e := tx.ExecContext(ctx, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES(?,?,?,'legacy-unverified','legacy-unverified','{}',?) ON CONFLICT(workspace_id,root_run_id) DO NOTHING`, id, workspace, runauthority.LegacyUnverifiedDelegationRootRunID, tsformat.Format(time.Now())); e != nil {
			return nil, e
		}
		if e := tx.QueryRowContext(ctx, `SELECT id FROM execution_origins WHERE workspace_id=? AND root_run_id=?`, workspace, runauthority.LegacyUnverifiedDelegationRootRunID).Scan(&id); e != nil {
			return nil, e
		}
		cache[workspace] = id
	}
	copyRow := make(map[string]any, len(row)+1)
	for k, v := range row {
		copyRow[k] = v
	}
	copyRow["chat_origin_id"] = id
	return copyRow, nil
}
