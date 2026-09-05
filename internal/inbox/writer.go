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
	"errors"
	"log/slog"
	"strings"
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
	// KindScheduleCircuitBreakerTripped surfaces a schedule auto-disabled
	// after N consecutive failures (#1405). The value was previously
	// written as a bare string literal in internal/pipeline/schedules.go
	// and was NEVER in the inbox_items.kind CHECK, so every insert failed
	// the constraint and the "your routine was disabled" alert reached
	// nobody. Requires migration v168.
	KindScheduleCircuitBreakerTripped = "schedule_circuit_breaker_tripped"
	// KindWebhookFireFailed surfaces a webhook whose fire has failed to
	// become a run repeatedly in a row (PRD A4 / F20). Written via Upsert,
	// not Insert: the same webhook can trip this more than once across its
	// life (fail, recover, fail again), and each trip is news about the
	// SAME subject rather than a new one-off event, so a resolved card is
	// resurrected to unread rather than silently swallowed by the unique
	// index. See alertWebhookFireFailure, internal/api/pipeline_webhooks.go.
	KindWebhookFireFailed = "webhook_fire_failed"
	// KindAutomationEnqueueFailed surfaces an automation rule that matched
	// but could not park its run repeatedly in a row (PRD A4 / F20) — the
	// automation-side twin of KindWebhookFireFailed, same Upsert-not-Insert
	// reasoning. See Registry.emitEnqueueFailed, internal/automation/registry.go.
	KindAutomationEnqueueFailed = "automation_enqueue_failed"
	// KindRunNeedsHuman surfaces a run (issue-session assignment, or
	// routine/pipeline run) whose §9.6 outcome contract came back
	// NEEDS_HUMAN — blocked on a decision, missing input, or a credential
	// (PRD-ISSUES-AND-ROUTINES-2026 §9.6/§12, work package B6, #2349).
	// Written via Insert, not Upsert: source_id is the run/assignment id,
	// which never fires twice for the SAME run, so there is nothing to
	// resurrect — a second NEEDS_HUMAN on the same subject is a NEW run
	// with its own id. Payload carries the §12 action contract
	// (attention_class, thread_key, actions, who_can_act, context).
	KindRunNeedsHuman = "run_needs_human"
)

// AllKinds is the canonical set of inbox_items.kind values the product
// writes. It is the single source of truth for the DB CHECK constraint:
// TestInboxKindsMatchSchema (internal/database) inserts one row per entry
// against the REAL migrated schema, so a kind added here without a
// matching migration fails CI rather than failing silently at runtime.
//
// That guard exists because the failure mode is invisible: Insert's error
// is logged, not propagated to the user, so a kind missing from the CHECK
// means an alert that simply never arrives.
var AllKinds = []string{
	KindWaitpoint,
	KindEscalation,
	KindFailedRun,
	KindMessage,
	KindMemoryConsolidation,
	KindScheduleMissed,
	KindScheduleCircuitBreakerTripped,
	KindWebhookFireFailed,
	KindAutomationEnqueueFailed,
	KindRunNeedsHuman,
}

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

// Action is one entry of the §12 attention contract's `actions[]` — a
// permitted, named thing a recipient can do about this item. Mirrors the
// PRD's own JSON shape verbatim (id/label/effect/irreversible) so the wire
// format and the Go type never drift.
type Action struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Effect       string `json:"effect,omitempty"`
	Irreversible bool   `json:"irreversible,omitempty"`
}

// AttentionClass is the closed §12 vocabulary for Item.AttentionClass — kept
// as named constants so a producer can't typo past the DB CHECK silently
// (an unrecognised value fails the CHECK loudly at insert time, same as an
// unrecognised Kind).
const (
	AttentionDecision = "decision"
	AttentionInput    = "input"
	AttentionReview   = "review"
	AttentionRepair   = "repair"
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

	// Category optionally names the notification category this item routes
	// under, overriding the kind→category mapping the router would apply.
	// It exists for producers that know what an event IS better than its
	// inbox kind can express — a routine's notify step emits kind "message"
	// whatever it is about, so without this every routine notice arrived as
	// a chat reply.
	//
	// IN-FLIGHT ONLY: not a column, not persisted. The resolved category is
	// stored on the delivery row, which is what the recovery sweep reads, so
	// there is nothing for the inbox table to remember. Empty = let the
	// router decide, which keeps that mapping in one place.
	//
	// This package stays a leaf and does not import internal/notify, so the
	// value is validated where it is authored, not here.
	Category string

	// ThreadKey, AttentionClass and Actions are the §12 attention contract
	// (PRD-ISSUES-AND-ROUTINES-2026, work package B10, #2364), promoted from
	// ad hoc payload fields (B6's run_needs_human was the one existing
	// example) to real, queryable columns.
	//
	// ThreadKey is the identity of the RECURRING CONDITION this item is
	// about, stable across every occurrence — "the same routine needs a
	// decision", not "this specific save call". Insert/Upsert persist it as
	// a column but do not act on it; WriteThreaded is the write path that
	// actually merges same-thread items (see its doc comment). Empty is
	// legal — most existing kinds this PR does not touch have no thread
	// concept yet — and callers that pass Insert/Upsert an empty ThreadKey
	// get the same (kind, source_id)-only dedup they always had.
	ThreadKey      string
	AttentionClass string // "" | decision | input | review | repair (see the Attention* constants)
	Actions        []Action
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
	payloadJSON := marshalPayload(in.Payload)
	actionsJSON := marshalActions(in.Actions)
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
			thread_key, attention_class, actions_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''),
			?, ?,
			NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
			'unread', ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''), ?,
			?, ?)`,
		id, in.WorkspaceID, in.Kind, in.SourceID,
		in.TargetUserID, in.TargetRole,
		in.Title, in.BodyMD,
		in.SenderType, in.SenderID, in.SenderName,
		in.Priority, blocking, string(payloadJSON),
		in.ThreadKey, in.AttentionClass, string(actionsJSON),
		now, now,
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

// UpsertMessage inserts a message-kind inbox row, refreshing an
// existing one in place. See Upsert — this name is kept for the
// chat-notification path it was written for.
func UpsertMessage(ctx context.Context, db *sql.DB, logger *slog.Logger, in Item) error {
	return Upsert(ctx, db, logger, in)
}

// Upsert inserts an inbox row, or — when a row with the same (kind,
// source_id) already exists — refreshes it in place: title/body/payload
// are replaced, timestamps bumped, and the row is resurrected to
// 'unread' with its read/resolved markers cleared, including every
// caller's OWN per-user inbox_item_reads marker (A7) — a resurrected
// item carries genuinely new content, so a user who read the PREVIOUS
// occurrence must see this one as unread too, not skip it because their
// old marker is still sitting on the row.
//
// This is the primitive for a source that can fire more than once about
// the SAME subject: repeated chat replies update ONE bell item instead
// of piling up siblings, and a routine proposed a second time asks for
// a decision again instead of being swallowed by the first item.
//
// Insert is the other half of that choice, and the two are not
// interchangeable. Insert's INSERT OR IGNORE says "this source_id
// identifies one event, so a repeat is a retry" — right for a hook that
// may be delivered twice. Upsert says "this source_id identifies a
// subject, and it has news" — right when the row's content, and whether
// anyone still needs to act on it, can change. Picking the wrong one is
// invisible until the second occurrence: with Insert it is silently
// dropped, and nobody is asked.
//
// Same envelope-validation contract as Insert: caller bugs (nil db,
// missing workspace/kind/source) are a silent no-op returning nil; real
// SQL failures are logged and returned.
func Upsert(ctx context.Context, db *sql.DB, logger *slog.Logger, in Item) error {
	if db == nil || in.WorkspaceID == "" || in.Kind == "" || in.SourceID == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if in.Priority == "" {
		in.Priority = "medium"
	}
	payloadJSON := marshalPayload(in.Payload)
	actionsJSON := marshalActions(in.Actions)
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
			thread_key, attention_class, actions_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''),
			?, ?,
			NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
			'unread', ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''), ?,
			?, ?)
		ON CONFLICT(kind, source_id) DO UPDATE SET
			title = excluded.title,
			body_md = excluded.body_md,
			sender_type = excluded.sender_type,
			sender_id = excluded.sender_id,
			sender_name = excluded.sender_name,
			priority = excluded.priority,
			blocking = excluded.blocking,
			payload_json = excluded.payload_json,
			thread_key = excluded.thread_key,
			attention_class = excluded.attention_class,
			actions_json = excluded.actions_json,
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
		in.Priority, blocking, string(payloadJSON),
		in.ThreadKey, in.AttentionClass, string(actionsJSON),
		now, now,
	)
	if err != nil {
		logger.Warn("inbox upsert", "error", err, "kind", in.Kind, "source_id", in.SourceID)
		return err
	}
	// Clear every per-user read marker (A7) alongside the shared columns
	// the UPSERT above just reset. Best-effort: the row itself already
	// resurrected correctly, so a failure here logs rather than fails the
	// whole call — worst case a stale per-user marker survives one refresh
	// cycle, not a lost notification.
	if _, iirErr := db.ExecContext(ctx, `DELETE FROM inbox_item_reads WHERE inbox_item_id = ?`, id); iirErr != nil {
		logger.Warn("inbox upsert: clear per-user read markers", "error", iirErr, "kind", in.Kind, "source_id", in.SourceID)
	}
	// Unlike Insert, Upsert always fans out — by design, a repeated call
	// here means a genuinely new event (another chat reply, another
	// proposal) refreshed an existing row rather than being ignored as a
	// duplicate. Callers scope SourceID to the subject, so this fires
	// once per real event, not once per row-write.
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

// marshalPayload is Insert/Upsert/WriteThreaded's shared encoding for
// Item.Payload — '{}' on nil or a marshal failure, matching the convention
// every existing caller already depends on (a producer that races a bad
// value into Payload gets an empty object, not a write failure).
func marshalPayload(payload map[string]interface{}) []byte {
	if payload == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// marshalActions is the actions_json equivalent of marshalPayload — '[]' on
// nil/empty or a marshal failure, never NULL, so every reader can
// json.Unmarshal it unconditionally (actions_json's own column default).
func marshalActions(actions []Action) []byte {
	if len(actions) == 0 {
		return []byte("[]")
	}
	b, err := json.Marshal(actions)
	if err != nil {
		return []byte("[]")
	}
	return b
}

// mergeJSONObjects layers `incoming` on top of `base` — `base`'s keys survive
// unless `incoming` sets the same key, in which case `incoming` wins. Used by
// WriteThreaded's merge branch so a second producer's write adds to what the
// first producer said instead of overwriting it (a routine's governance
// review payload keeps its risk_reasons even after the trigger-activation
// review contributes routine_version/first_fire_at into the SAME row).
// Malformed JSON on either side is treated as empty rather than failing the
// whole write — a merge is a best-effort enrichment, not a new source of
// truth.
func mergeJSONObjects(baseJSON, incomingJSON string) string {
	base := map[string]interface{}{}
	_ = json.Unmarshal([]byte(baseJSON), &base)
	incoming := map[string]interface{}{}
	_ = json.Unmarshal([]byte(incomingJSON), &incoming)
	for k, v := range incoming {
		base[k] = v
	}
	out, err := json.Marshal(base)
	if err != nil {
		return incomingJSON
	}
	return string(out)
}

// mergeActionsJSON unions two actions_json arrays by Action.ID — existing
// actions are kept in place, an incoming action with a NEW id is appended,
// and an incoming action reusing an EXISTING id replaces it in place (a
// producer refreshing its own action's label/effect on a later call, e.g. a
// newer routine_version changing what "Approve" would do).
func mergeActionsJSON(baseJSON, incomingJSON string) string {
	var base, incoming []Action
	_ = json.Unmarshal([]byte(baseJSON), &base)
	_ = json.Unmarshal([]byte(incomingJSON), &incoming)
	if len(incoming) == 0 {
		return string(marshalActions(base))
	}
	byID := make(map[string]int, len(base))
	for i, a := range base {
		byID[a.ID] = i
	}
	for _, a := range incoming {
		if i, ok := byID[a.ID]; ok {
			base[i] = a
		} else {
			base = append(base, a)
		}
	}
	return string(marshalActions(base))
}

// threadedRow is what WriteThreaded reads back about an existing open
// thread-mate before deciding how to merge into it.
type threadedRow struct {
	id          string
	title       string
	bodyMD      sql.NullString
	payloadJSON string
	actionsJSON string
}

// WriteThreaded is the write path for every producer that can name a
// thread_key (§12): the a4 trigger-failure kinds, B6's run_needs_human
// contract, B8's routine receipts (governance review + trigger-activation
// review), and the escalation/waitpoint producers that raise a review item
// for a subject that can recur. It is the server-side merge B10 replaces
// the client merge with (F28).
//
// When NO open (non-resolved) item exists yet in this workspace under
// in.ThreadKey, this behaves exactly like Upsert — a normal (kind,
// source_id)-keyed row is written, now carrying the thread_key for the
// NEXT call to find.
//
// When an open item already shares in.ThreadKey, this UPDATES that row IN
// PLACE instead of raising a sibling card — the fix for the exact duplicate
// #2364's live check found (a `routine save --draft` raising both a
// governance "proposed for review" card and a B8 "trigger ready" receipt for
// the SAME routine, because the two producers shared no identity beyond
// "the same routine"). The existing row's OWN (kind, source_id) is left
// untouched — it is still what that row's own producer resolves via
// ResolveBySource — and:
//   - title stays the FIRST producer's (the earlier, usually broader,
//     question — e.g. "may this routine run at all" before "may its
//     trigger fire")
//   - body_md gets the incoming producer's body appended as a new
//     paragraph, unless it is already present (repeat calls, e.g. the
//     same condition recurring across several days, must not keep growing
//     the card)
//   - payload_json is merged key-by-key, incoming wins on collision
//     (mergeJSONObjects) — this is how routine_version reaches the row
//     regardless of which producer's call carried it
//   - actions_json is merged by action id (mergeActionsJSON) — the merged
//     card offers every action either producer contributed
//   - attention_class takes the incoming value when non-empty (a later,
//     more specific class supersedes an earlier default), otherwise keeps
//     the existing one
//   - the row is resurrected to unread and every per-user read marker
//     cleared (A7), matching Upsert — new content means a user who already
//     read the old occurrence has not read this one
//
// A caller resolving the thread later must use ResolveByThreadOrSource, not
// ResolveBySource: the row's stored identity may belong to a DIFFERENT
// producer than the one now trying to resolve it.
func WriteThreaded(ctx context.Context, db *sql.DB, logger *slog.Logger, in Item) error {
	if db == nil || in.WorkspaceID == "" || in.Kind == "" || in.SourceID == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if in.ThreadKey == "" {
		// No thread concept for this call — fall back to the (kind,
		// source_id) dedup every existing caller already understands.
		return Upsert(ctx, db, logger, in)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		logger.Warn("inbox write-threaded: begin tx", "error", err, "thread_key", in.ThreadKey)
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var existing threadedRow
	var bodyMD sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT id, title, body_md, payload_json, actions_json
		  FROM inbox_items
		 WHERE workspace_id = ? AND thread_key = ? AND state != 'resolved'
		 ORDER BY created_at ASC, id ASC LIMIT 1`,
		in.WorkspaceID, in.ThreadKey).Scan(&existing.id, &existing.title, &bodyMD, &existing.payloadJSON, &existing.actionsJSON)
	existing.bodyMD = bodyMD

	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := upsertRowTx(ctx, tx, in); err != nil {
			logger.Warn("inbox write-threaded: insert", "error", err, "thread_key", in.ThreadKey)
			return err
		}
	case err != nil:
		logger.Warn("inbox write-threaded: lookup", "error", err, "thread_key", in.ThreadKey)
		return err
	default:
		if err := mergeThreadedRowTx(ctx, tx, existing, in); err != nil {
			logger.Warn("inbox write-threaded: merge", "error", err, "thread_key", in.ThreadKey, "id", existing.id)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		logger.Warn("inbox write-threaded: commit", "error", err, "thread_key", in.ThreadKey)
		return err
	}
	committed = true
	// Same fan-out rule as Upsert: every WriteThreaded call describes a real
	// event (a new occurrence, or new information about an open one), never
	// a retried duplicate, so it always notifies.
	notifyExternal(ctx, in)
	return nil
}

// upsertRowTx is Upsert's insert/on-conflict statement, extracted so
// WriteThreaded's "no open thread-mate yet" branch shares it instead of
// duplicating the column list a third time. Runs on the caller's
// transaction.
func upsertRowTx(ctx context.Context, tx *sql.Tx, in Item) error {
	if in.Priority == "" {
		in.Priority = "medium"
	}
	payloadJSON := marshalPayload(in.Payload)
	actionsJSON := marshalActions(in.Actions)
	id := "ibx_" + in.Kind + "_" + in.SourceID
	now := tsformat.Format(time.Now())
	blocking := 0
	if in.Blocking {
		blocking = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO inbox_items (
			id, workspace_id, kind, source_id,
			target_user_id, target_role,
			title, body_md,
			sender_type, sender_id, sender_name,
			state, priority, blocking, payload_json,
			thread_key, attention_class, actions_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''),
			?, ?,
			NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
			'unread', ?, ?, ?,
			NULLIF(?, ''), NULLIF(?, ''), ?,
			?, ?)
		ON CONFLICT(kind, source_id) DO UPDATE SET
			title = excluded.title,
			body_md = excluded.body_md,
			sender_type = excluded.sender_type,
			sender_id = excluded.sender_id,
			sender_name = excluded.sender_name,
			priority = excluded.priority,
			blocking = excluded.blocking,
			payload_json = excluded.payload_json,
			thread_key = excluded.thread_key,
			attention_class = excluded.attention_class,
			actions_json = excluded.actions_json,
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
		in.Priority, blocking, string(payloadJSON),
		in.ThreadKey, in.AttentionClass, string(actionsJSON),
		now, now,
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM inbox_item_reads WHERE inbox_item_id = ?`, id)
	return err
}

// mergeThreadedRowTx updates an existing open thread-mate in place — see
// WriteThreaded's doc comment for exactly what merges vs. what is
// preserved. kind/source_id are deliberately NOT touched: the row's
// resolve identity must keep answering to whichever producer originally
// raised it, which is why a caller resolving a merged thread must use
// ResolveByThreadOrSource rather than assume its own (kind, source_id).
func mergeThreadedRowTx(ctx context.Context, tx *sql.Tx, existing threadedRow, in Item) error {
	if in.Priority == "" {
		in.Priority = "medium"
	}
	title := existing.title
	if title == "" {
		title = in.Title
	}
	body := existing.bodyMD.String
	if in.BodyMD != "" && !strings.Contains(body, in.BodyMD) {
		if body != "" {
			body = body + "\n\n---\n\n" + in.BodyMD
		} else {
			body = in.BodyMD
		}
	}
	mergedPayload := mergeJSONObjects(existing.payloadJSON, string(marshalPayload(in.Payload)))
	mergedActions := mergeActionsJSON(existing.actionsJSON, string(marshalActions(in.Actions)))
	attentionClass := in.AttentionClass

	now := tsformat.Format(time.Now())
	_, err := tx.ExecContext(ctx, `
		UPDATE inbox_items SET
			title = ?,
			body_md = ?,
			sender_type = COALESCE(NULLIF(?, ''), sender_type),
			sender_id = COALESCE(NULLIF(?, ''), sender_id),
			sender_name = COALESCE(NULLIF(?, ''), sender_name),
			priority = ?,
			blocking = ?,
			payload_json = ?,
			attention_class = COALESCE(NULLIF(?, ''), attention_class),
			actions_json = ?,
			state = 'unread',
			read_at = NULL,
			read_by_user_id = NULL,
			resolved_at = NULL,
			resolved_by_user_id = NULL,
			resolved_action = NULL,
			updated_at = ?
		WHERE id = ?`,
		title, body,
		in.SenderType, in.SenderID, in.SenderName,
		in.Priority, boolToInt(in.Blocking), mergedPayload,
		attentionClass, mergedActions,
		now, existing.id,
	)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM inbox_item_reads WHERE inbox_item_id = ?`, existing.id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ResolveByThreadOrSource resolves an inbox item either by its OWN (kind,
// source_id) — the ordinary case, identical to ResolveBySource — or, when
// that finds nothing, by (workspace_id, thread_key). The fallback matters
// exactly when WriteThreaded has merged two producers' items into one row:
// the row's stored identity belongs to whichever producer wrote it FIRST,
// so the second producer's own decision route (e.g. POST .../activate)
// would otherwise resolve nothing and leave a decided card looking open.
// threadKey may be empty for a caller with no thread concept, in which case
// this is exactly ResolveBySource.
func ResolveByThreadOrSource(ctx context.Context, db *sql.DB, logger *slog.Logger, workspaceID, kind, sourceID, threadKey, action, userID string) {
	if db == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: resolved_at is read back for display, never compared in SQL — matches ResolveBySource
	res, err := db.ExecContext(ctx, `
		UPDATE inbox_items
		SET state = 'resolved',
		    resolved_at = COALESCE(resolved_at, ?),
		    resolved_by_user_id = COALESCE(resolved_by_user_id, NULLIF(?, '')),
		    resolved_action = COALESCE(resolved_action, NULLIF(?, '')),
		    updated_at = ?
		WHERE kind = ? AND source_id = ? AND state != 'resolved'`,
		now, userID, action, now, kind, sourceID)
	if err != nil {
		logger.Warn("inbox resolve by thread or source", "error", err, "kind", kind, "source_id", sourceID)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 || threadKey == "" || workspaceID == "" {
		return
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE inbox_items
		SET state = 'resolved',
		    resolved_at = COALESCE(resolved_at, ?),
		    resolved_by_user_id = COALESCE(resolved_by_user_id, NULLIF(?, '')),
		    resolved_action = COALESCE(resolved_action, NULLIF(?, '')),
		    updated_at = ?
		WHERE workspace_id = ? AND thread_key = ? AND state != 'resolved'`,
		now, userID, action, now, workspaceID, threadKey); err != nil {
		logger.Warn("inbox resolve by thread or source: thread fallback", "error", err, "thread_key", threadKey)
	}
}
