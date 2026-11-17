package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/provider"
)

// RecordCodexSandboxPreflight atomically persists compliance and the configured
// journal chain. It never receives model text or credential material.
func RecordCodexSandboxPreflight(ctx context.Context, db *sql.DB, emitter journal.Emitter, e provider.CodexSandboxEvidence) error {
	if e.WorkspaceID == "" || e.CrewID == "" || e.AgentID == "" || e.RunID == "" || e.Source != "crew_opt_in" || e.Mode != "danger-full-access" || (e.Status != "admitted" && e.Status != "denied") {
		return errors.New("invalid Codex sandbox audit attribution")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT c.allow_codex_outer_sandbox FROM crews c JOIN agents a ON a.crew_id=c.id AND a.workspace_id=c.workspace_id WHERE c.id=? AND c.workspace_id=? AND a.id=? AND c.deleted_at IS NULL AND a.deleted_at IS NULL`, e.CrewID, e.WorkspaceID, e.AgentID).Scan(&enabled)
	if err != nil {
		return err
	}
	if e.Status == "admitted" && !enabled {
		return errors.New("Codex outer isolation consent revoked before audit commit")
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	id, stamp := generateCUID(), time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,'codex.sandbox_preflight','RUN',?,?,?)`, id, e.WorkspaceID, e.RunID, string(raw), stamp)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.New("Codex sandbox compliance audit not inserted")
	}
	notify, err := appendAuthorizationAudit(ctx, tx, emitter, journal.Entry{WorkspaceID: e.WorkspaceID, CrewID: e.CrewID, AgentID: e.AgentID, Type: classifyAuditAction("codex.sandbox_preflight"), Severity: journal.SeverityNotice, ActorType: journal.ActorSystem, Summary: "Codex outer sandbox preflight " + e.Status, Payload: map[string]any{"action": "codex.sandbox_preflight", "entity_type": "RUN", "entity_id": e.RunID, "metadata": e}})
	if err != nil {
		return err
	}
	var intact bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audit_logs WHERE id=? AND workspace_id=? AND action='codex.sandbox_preflight' AND entity_type='RUN' AND entity_id=? AND metadata=? AND created_at=?)`, id, e.WorkspaceID, e.RunID, string(raw), stamp).Scan(&intact); err != nil || !intact {
		return errors.New("Codex sandbox audit did not persist intact")
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	notify()
	return nil
}
