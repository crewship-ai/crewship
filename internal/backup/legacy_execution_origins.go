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
	if workspace == "" || !allowed["chat_origin_id"] {
		return nil, fmt.Errorf("backup: unsupported authority schema: cannot fence legacy %s", table)
	}
	root, _ := row["chain_origin"].(string)
	if root == "" {
		key := "triggered_by_id"
		if table == "assignments" {
			key = "parent_run_id"
		}
		if table == "missions" {
			key = "trace_id"
		}
		root, _ = row[key].(string)
	}
	if root == "" && table == "pipeline_runs" {
		root, _ = row["id"].(string)
	}
	evidence, err := readLegacyOriginEvidence(ctx, tx, workspace, root)
	if err != nil {
		return nil, err
	}
	if evidence.autonomous {
		return row, nil
	}
	if evidence.root != "" {
		id := newRemapCUID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(workspace_id,root_run_id) DO NOTHING`, id, workspace, evidence.root, evidence.chat, evidence.message, evidence.receipt, tsformat.Format(time.Now())); err != nil {
			return nil, err
		}
		var stored string
		if err = tx.QueryRowContext(ctx, `SELECT id FROM execution_origins WHERE workspace_id=? AND root_run_id=? AND chat_id=? AND trigger_message_id=? AND human_authority=?`, workspace, evidence.root, evidence.chat, evidence.message, evidence.receipt).Scan(&stored); err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			copyRow := make(map[string]any, len(row)+1)
			for k, v := range row {
				copyRow[k] = v
			}
			copyRow["chat_origin_id"] = stored
			return copyRow, nil
		}
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
