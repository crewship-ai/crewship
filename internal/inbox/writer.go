// Package inbox provides the write-through helpers that source-of-
// truth handlers (waitpoint creator, escalation handler, pipeline
// run terminal) call to keep the unified inbox_items table in sync.
//
// This package owns ONLY the write-through projection — reads, list,
// and state transitions live in internal/api so they can use the
// HTTP context + auth infrastructure. Handlers in pipeline/api/etc.
// don't import each other, so the writer lives here in a leaf package
// every layer can import without cycles.
package inbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// Kind constants enumerate the inbox_items.kind CHECK values. Callers
// should use these so a typo can't quietly write a row with a kind the
// list endpoint won't render. Keep these in sync with the DB CHECK
// (currently widened by migration v90 to admit KindMemoryConsolidation).
const (
	KindWaitpoint           = "waitpoint"
	KindEscalation          = "escalation"
	KindFailedRun           = "failed_run"
	KindMessage             = "message"
	KindMemoryConsolidation = "memory_consolidation"
)

// Item is the payload passed to Insert. The exported fields map 1:1
// onto inbox_items columns; the writer fills in the deterministic
// id, state ('unread'), and timestamps so callers don't repeat that
// boilerplate.
type Item struct {
	WorkspaceID  string
	Kind         string                 // 'waitpoint' | 'escalation' | 'failed_run' | 'message'
	SourceID     string                 // back-pointer to authoritative row
	TargetUserID string                 // empty = anyone in workspace
	TargetRole   string                 // 'OWNER' | 'MANAGER' | empty
	Title        string                 // human-readable summary line
	BodyMD       string                 // markdown body (optional)
	SenderType   string                 // 'agent' | 'crew' | 'system' | 'pipeline'
	SenderID     string                 //
	SenderName   string                 //
	Priority     string                 // urgent | high | medium | low — defaults to medium
	Blocking     bool                   // true = needs explicit action
	Payload      map[string]interface{} // kind-specific structured data
}

// Insert persists a new inbox row. INSERT OR IGNORE so the
// (kind, source_id) unique index is the dedup key — the same source
// firing twice (retried hook, replay) doesn't duplicate rows.
//
// Returns the SQL error (if any) so callers that want to surface
// inbox-write failure (e.g. routine sweeps that would otherwise log a
// false-success summary) can propagate it. The writer still logs on
// failure so legacy callers that ignore the return value keep their
// existing log surface intact.
//
// The inbox is a projection; the source table remains the source of
// truth until phase 2 of the migration. Validation failures on the
// envelope (nil db, empty workspace_id/kind/source_id) return nil
// because they're caller bugs not transient SQL issues — callers can
// guard themselves; we just silently no-op rather than panic.
func Insert(ctx context.Context, db *sql.DB, logger *slog.Logger, in Item) error {
	if db == nil || in.WorkspaceID == "" || in.Kind == "" || in.SourceID == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	payloadJSON := []byte("{}")
	if in.Payload != nil {
		if b, err := json.Marshal(in.Payload); err == nil {
			payloadJSON = b
		}
	}
	id := "ibx_" + in.Kind + "_" + in.SourceID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	blocking := 0
	if in.Blocking {
		blocking = 1
	}
	_, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO inbox_items (
			id, workspace_id, kind, source_id,
			target_user_id, target_role,
			title, body_md,
			sender_type, sender_id, sender_name,
			state, priority, blocking, payload_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''),
			?, ?,
			NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
			'unread', ?, ?, ?, ?, ?)`,
		id, in.WorkspaceID, in.Kind, in.SourceID,
		in.TargetUserID, in.TargetRole,
		in.Title, in.BodyMD,
		in.SenderType, in.SenderID, in.SenderName,
		in.Priority, blocking, string(payloadJSON), now, now,
	)
	if err != nil {
		logger.Warn("inbox insert", "error", err, "kind", in.Kind, "source_id", in.SourceID)
		return err
	}
	return nil
}

// ResolveBySource flips an inbox item to state=resolved when the
// underlying source resolves (waitpoint approved/denied, escalation
// closed, failed run cancelled). resolved_action records what the
// user did so the audit trail matches the source table's lifecycle.
// Idempotent — safe to call from multiple terminal paths.
func ResolveBySource(ctx context.Context, db *sql.DB, logger *slog.Logger, kind, sourceID, action, userID string) {
	if db == nil || kind == "" || sourceID == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `
		UPDATE inbox_items
		SET state = 'resolved',
		    resolved_at = COALESCE(resolved_at, ?),
		    resolved_by_user_id = COALESCE(resolved_by_user_id, NULLIF(?, '')),
		    resolved_action = COALESCE(resolved_action, NULLIF(?, '')),
		    updated_at = ?
		WHERE kind = ? AND source_id = ? AND state != 'resolved'`,
		now, userID, action, now, kind, sourceID)
	if err != nil {
		logger.Warn("inbox resolve", "error", err, "kind", kind, "source_id", sourceID)
	}
}

// ResolveByPipeline resolves every still-open inbox item tied to a routine
// that was just deleted, so a removed routine doesn't leave dangling review
// escalations, failed-run alerts, or pending waitpoints in the inbox forever
// (38 deleted routines were still showing "proposed for review" escalations).
// It matches, scoped to the workspace, any non-resolved row whose payload
// carries this pipeline id (json $.pipeline_id — the proposed-review
// escalation + scheduled failed-run alerts) OR one of the pipeline's run ids
// (json $.pipeline_run_id — waitpoints raised mid-run). Idempotent and
// best-effort: a projection failure is logged, not fatal, since the pipeline
// row (the source of truth) is already soft-deleted.
func ResolveByPipeline(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID, pipelineID, action, userID string) {
	if db == nil || workspaceID == "" || pipelineID == "" {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.ExecContext(ctx, `
		UPDATE inbox_items
		SET state = 'resolved',
		    resolved_at = COALESCE(resolved_at, ?),
		    resolved_by_user_id = COALESCE(resolved_by_user_id, NULLIF(?, '')),
		    resolved_action = COALESCE(resolved_action, NULLIF(?, '')),
		    updated_at = ?
		WHERE workspace_id = ?
		  AND state != 'resolved'
		  AND (
		      json_extract(payload_json, '$.pipeline_id') = ?
		      OR json_extract(payload_json, '$.pipeline_run_id') IN (
		          SELECT id FROM pipeline_runs WHERE pipeline_id = ?
		      )
		  )`,
		now, userID, action, now, workspaceID, pipelineID, pipelineID)
	if err != nil {
		logger.Warn("inbox resolve by pipeline", "error", err, "pipeline_id", pipelineID)
	}
}
