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

	"github.com/crewship-ai/crewship/internal/tsformat"
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
	// KindScheduleMissed surfaces a schedule that dropped or reported
	// overdue cron occurrences per its catchup_policy (#1422 item 2).
	// Requires migration v155 (widens the inbox_items.kind CHECK).
	KindScheduleMissed = "schedule_missed"
)

// ExternalNotifier is the injected seam that fans a freshly-committed
// inbox item out to a recipient's EXTERNAL notification channels — email /
// webhook / Slack / Discord / Telegram, per their category × channel
// preference matrix (issue #1412). This is the single chokepoint the
// design calls for: Insert and UpsertMessage are already the funnel every
// inbox-writing call site (waitpoint, escalation, failed_run, message,
// consolidation — ~13 call sites) goes through, so hooking here reaches
// all of them without touching any of them.
//
// Kept as a minimal interface (Item in, nothing out) rather than importing
// internal/notify — this package is a deliberate leaf (see the package
// doc) that every layer imports without cycles; the concrete
// implementation (internal/notifyroute.Router) is wired at server boot via
// SetExternalNotifier, exactly like RunStore.SetTerminalNotifier wires the
// #850 run-terminal fan-out. The nil zero value is a safe no-op so every
// existing caller keeps working unchanged on a boot path that hasn't wired
// a notifier (tests, `crewship seed`, etc).
//
// Implementations MUST be fire-and-forget: NotifyInboxItem is called
// inline on the writer's hot path (an HTTP handler, a pipeline step), so a
// slow or blocking implementation would slow down every inbox write in the
// product. internal/notifyroute.Router dispatches through its own
// goroutine internally for exactly this reason.
type ExternalNotifier interface {
	NotifyInboxItem(ctx context.Context, item Item)
}

// externalNotifier is the process-wide hook, set once at boot before the
// server starts accepting traffic (mirrors webhookTransport-style package
// vars elsewhere in this codebase). Not mutex-guarded: production sets it
// exactly once during wiring, before any request can reach Insert/
// UpsertMessage; tests that need isolation use SetExternalNotifierForTesting.
var externalNotifier ExternalNotifier

// SetExternalNotifier wires the production external-notification fan-out.
// Called once at boot (cmd_start.go). Passing nil restores the no-op
// default.
func SetExternalNotifier(n ExternalNotifier) { externalNotifier = n }

// SetExternalNotifierForTesting swaps the notifier and returns a restore
// func, for tests in OTHER packages that need to assert on the fan-out
// without a real boot sequence.
func SetExternalNotifierForTesting(n ExternalNotifier) func() {
	prev := externalNotifier
	externalNotifier = n
	return func() { externalNotifier = prev }
}

// notifyExternal calls the wired notifier, if any. A nil interface (no
// notifier wired) or nil db is silently skipped — matches the rest of this
// file's "caller bugs / unwired paths are a no-op, not a panic" contract.
func notifyExternal(ctx context.Context, item Item) {
	if externalNotifier == nil {
		return
	}
	externalNotifier.NotifyInboxItem(ctx, item)
}

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
	// Fixed-width sortable form: every inbox_items writer (here + the hire
	// path in internal/api/agents_hire.go) must agree on this format so the
	// (workspace_id, state, created_at DESC) index orders correctly across
	// writers. A trailing-zero-trimmed nano form is variable width and would
	// mis-sort against a fixed-width row inside the same second.
	now := tsformat.Format(time.Now())
	blocking := 0
	if in.Blocking {
		blocking = 1
	}
	res, err := db.ExecContext(ctx, `
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
	// Fan out to external channels ONLY when a NEW row was actually
	// written — INSERT OR IGNORE makes a retried/duplicate source_id a
	// no-op, and a no-op must not re-push a notification that already
	// went out on the first call (mirrors the dedup contract the (kind,
	// source_id) unique index already gives the in-product inbox).
	if n, _ := res.RowsAffected(); n > 0 {
		notifyExternal(ctx, in)
	}
	return nil
}

// UpsertMessage inserts a message-kind inbox row, or — when a row with
// the same (kind, source_id) already exists — refreshes it in place:
// title/body/payload are replaced, timestamps bumped, and the row is
// resurrected to 'unread' with its read/resolved markers cleared. This
// is the dedupe primitive behind per-(user, chat) notifications like
// "your agent replied": repeated replies update ONE bell item instead
// of piling up siblings, and a new reply after the user dismissed the
// old item correctly re-notifies.
//
// Same envelope-validation contract as Insert: caller bugs (nil db,
// missing workspace/kind/source) are a silent no-op returning nil; real
// SQL failures are logged and returned.
func UpsertMessage(ctx context.Context, db *sql.DB, logger *slog.Logger, in Item) error {
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
	// Fixed-width sortable form — see Insert for why every inbox_items writer
	// must share this format for the created_at index to order correctly.
	now := tsformat.Format(time.Now())
	blocking := 0
	if in.Blocking {
		blocking = 1
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO inbox_items (
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
			'unread', ?, ?, ?, ?, ?)
		ON CONFLICT(kind, source_id) DO UPDATE SET
			title = excluded.title,
			body_md = excluded.body_md,
			sender_type = excluded.sender_type,
			sender_id = excluded.sender_id,
			sender_name = excluded.sender_name,
			priority = excluded.priority,
			blocking = excluded.blocking,
			payload_json = excluded.payload_json,
			state = 'unread',
			read_at = NULL,
			read_by_user_id = NULL,
			resolved_at = NULL,
			resolved_by_user_id = NULL,
			resolved_action = NULL,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		id, in.WorkspaceID, in.Kind, in.SourceID,
		in.TargetUserID, in.TargetRole,
		in.Title, in.BodyMD,
		in.SenderType, in.SenderID, in.SenderName,
		in.Priority, blocking, string(payloadJSON), now, now,
	)
	if err != nil {
		logger.Warn("inbox upsert", "error", err, "kind", in.Kind, "source_id", in.SourceID)
		return err
	}
	// Unlike Insert, UpsertMessage always fans out — by design, a repeated
	// call here means a genuinely new event (another chat reply) refreshed
	// an existing row rather than being ignored as a duplicate; the caller
	// (chatnotify) already scopes SourceID per (chat, recipient) so this
	// fires once per real reply per recipient, not once per row-write.
	notifyExternal(ctx, in)
	return nil
}

// ResolveBySource flips an inbox item to state=resolved when the
// underlying source resolves (waitpoint approved/denied, escalation
// closed, failed run cancelled). resolved_action records what the
// user did so the audit trail matches the source table's lifecycle.
// Idempotent — safe to call from multiple terminal paths.
func ResolveBySource(ctx context.Context, db *sql.DB, logger *slog.Logger, kind, sourceID, action, userID string) {
	if db == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := ResolveBySourceTx(ctx, db, kind, sourceID, action, userID); err != nil {
		logger.Warn("inbox resolve", "error", err, "kind", kind, "source_id", sourceID)
	}
}

// DBTX is the subset of *sql.DB / *sql.Tx the write-through helpers
// need — it lets ResolveBySourceTx ride a caller-owned transaction
// while ResolveBySource keeps its own autocommit + log-and-swallow
// contract. Same shape as auditExecer in internal/api/credential_audit.go.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ResolveBySourceTx is ResolveBySource on the CALLER's transaction, and
// it RETURNS the error instead of logging it. Handlers whose source-of-
// truth mutation must not commit without the matching projection
// (ephemeral-hire decisions, issue #1247) use this so a failed inbox
// write rolls the whole decision back rather than stranding an
// unresolved blocking waitpoint against a terminal approval.
func ResolveBySourceTx(ctx context.Context, tx DBTX, kind, sourceID, action, userID string) error {
	if tx == nil || kind == "" || sourceID == "" {
		return nil
	}
	// Encoding must stay byte-identical to ResolveBySource below, which this
	// function was extracted from — otherwise resolved_at carries two formats
	// depending on which variant wrote the row.
	now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: matches the autocommit ResolveBySource this was extracted from; resolved_at is read back for display, never compared in SQL
	_, err := tx.ExecContext(ctx, `
		UPDATE inbox_items
		SET state = 'resolved',
		    resolved_at = COALESCE(resolved_at, ?),
		    resolved_by_user_id = COALESCE(resolved_by_user_id, NULLIF(?, '')),
		    resolved_action = COALESCE(resolved_action, NULLIF(?, '')),
		    updated_at = ?
		WHERE kind = ? AND source_id = ? AND state != 'resolved'`,
		now, userID, action, now, kind, sourceID)
	return err
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
