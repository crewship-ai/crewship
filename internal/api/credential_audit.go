package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Credential audit & last-used signal — backs the row-level "is this
// credential still in use?" affordance plus the inline Audit tab in
// the detail Sheet (CONNECTIONS.md §4.2, §4.3).
//
// Two persistence layers cooperate:
//  1. credentials.last_used_at + credentials.last_used_ips — denormalised
//     snapshot for fast list-row rendering. Updated on every USE event,
//     debounced at the call site (sidecar) to avoid burning writes on
//     hot credentials.
//  2. credential_audit — full event timeline (USE, ROTATE, TEST,
//     REVOKE, DETECTED). Append-only.

const (
	// Ringbuffer cap for credentials.last_used_ips. Matches GitLab's
	// "last 5 IPs" UX pattern referenced in CONNECTIONS.md §3.5; the
	// schema stores a JSON array TEXT, the cap is enforced here in Go.
	lastUsedIPRingSize = 5

	// Default page size for the audit timeline endpoint. The Doppler
	// inline drawer pattern that we're cribbing renders 50 events at
	// a time and lazy-loads on scroll.
	auditDefaultLimit = 50
	auditMaxLimit     = 500
)

// CredentialAuditEvent is the supported set of event_type values.
// Validated in RecordCredentialEvent — the column itself is
// schema-free so adding a new event class never requires a migration.
type CredentialAuditEvent string

const (
	AuditEventUse      CredentialAuditEvent = "USE"
	AuditEventRotate   CredentialAuditEvent = "ROTATE"
	AuditEventTest     CredentialAuditEvent = "TEST"
	AuditEventRevoke   CredentialAuditEvent = "REVOKE"
	AuditEventDetected CredentialAuditEvent = "DETECTED"
	AuditEventCreated  CredentialAuditEvent = "CREATED"
	// Approval lifecycle for agent-proposed credentials (v119): an agent
	// proposes a credential (CREATED, status PENDING_APPROVAL) and a human
	// then APPROVED it (→ ACTIVE) or REJECTED it (→ soft-deleted).
	AuditEventApproved CredentialAuditEvent = "APPROVED"
	AuditEventRejected CredentialAuditEvent = "REJECTED"
	// AuditEventLeased (#1373): the credential's grant to an agent was
	// (re-)issued as a short-lived lease — by an operator's explicit --ttl, or
	// automatically on a Keeper ALLOW / escalation approve when the workspace
	// has auto_lease_seconds configured. The metadata carries the source, the
	// resulting expiry and the authorising request id, so "why did this
	// credential stop working at 14:32?" is answerable from the timeline.
	AuditEventLeased CredentialAuditEvent = "LEASED"
	// AuditEventReveal (PRD-CREDENTIALS-V2-2026 §2.6 L4): the plaintext
	// was disclosed to a human through POST /credentials/{id}/reveal.
	//
	// This table is NOT the authoritative record of a reveal — it has no
	// hash chain, so a row can be deleted without trace. The tamper-evident
	// copy lives in internal/journal as credential.revealed and its write
	// is a precondition of the disclosure. This row exists so the reveal
	// shows up in the credential detail Sheet's timeline alongside every
	// other event on the same credential, which is where an operator
	// actually looks first.
	//
	// Metadata carries who/why/classification and the journal entry id that
	// anchors it to the chain. Never the value.
	AuditEventReveal CredentialAuditEvent = "REVEAL"
)

var validAuditEvents = map[CredentialAuditEvent]struct{}{
	AuditEventUse:      {},
	AuditEventRotate:   {},
	AuditEventTest:     {},
	AuditEventRevoke:   {},
	AuditEventDetected: {},
	AuditEventCreated:  {},
	AuditEventApproved: {},
	AuditEventRejected: {},
	AuditEventLeased:   {},
	AuditEventReveal:   {},
}

// credentialAuditDropped counts audit events that a best-effort call
// site failed to persist (see recordCredentialEventBestEffort). The
// mutation the event describes already succeeded, so the row is gone
// for good — this counter is the "lost compliance event" signal the
// TODO at the Create call site asked for, exposed on /metrics as
// crewshipd_credential_audit_dropped_total.
var credentialAuditDropped atomic.Int64

// CredentialAuditDroppedTotal returns the number of credential audit
// events dropped by best-effort writers since process start. Read by
// the /metrics handler in internal/server.
func CredentialAuditDroppedTotal() int64 {
	return credentialAuditDropped.Load()
}

// auditExecer is the subset of *sql.DB / *sql.Tx the audit writers
// need — lets RecordCredentialEventTx ride a caller-owned transaction
// while RecordCredentialEvent keeps managing its own.
type auditExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// RecordCredentialEventTx appends one row to credential_audit — and,
// for USE events, refreshes the denormalised last_used_at +
// last_used_ips snapshot — using the CALLER's transaction. Commit and
// rollback stay with the caller: a mutation handler that already runs
// in a tx includes its audit row atomically, so the timeline can never
// silently miss an event the mutation succeeded for (audit fails →
// whole mutation rolls back).
//
// ip is optional — empty string is stored as NULL. metadata is
// optional — pass nil for events whose type alone is sufficient.
func RecordCredentialEventTx(
	ctx context.Context,
	tx auditExecer,
	credentialID string,
	event CredentialAuditEvent,
	agentID string,
	ip string,
	metadata map[string]any,
) error {
	if _, ok := validAuditEvents[event]; !ok {
		return fmt.Errorf("invalid audit event %q", event)
	}
	if credentialID == "" {
		return errors.New("credentialID required")
	}

	var metaJSON sql.NullString
	if len(metadata) > 0 {
		raw, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal audit metadata: %w", err)
		}
		metaJSON = sql.NullString{Valid: true, String: string(raw)}
	}

	var agentArg sql.NullString
	if agentID != "" {
		agentArg = sql.NullString{Valid: true, String: agentID}
	}

	var ipArg sql.NullString
	if ip != "" {
		ipArg = sql.NullString{Valid: true, String: ip}
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// workspace_id is derived from the credential in the same statement
	// rather than taken as a parameter. Two reasons: no caller has to know
	// the workspace to write an audit row (there are a dozen call sites and
	// several only hold a credential id), and the column physically cannot
	// disagree with the credential it describes — which is the failure a
	// denormalised tenant column invites. See the migration
	// 20260810153104_credential_audit_workspace_scope for why the column
	// exists at all.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credential_audit (id, credential_id, workspace_id, event_type, agent_id, ip_address, metadata_json, occurred_at)
		VALUES (?, ?, (SELECT workspace_id FROM credentials WHERE id = ?), ?, ?, ?, ?, ?)`,
		generateCUID(), credentialID, credentialID, string(event), agentArg, ipArg, metaJSON, now); err != nil {
		return fmt.Errorf("insert audit row: %w", err)
	}

	// USE events refresh the denormalised snapshot. ROTATE/TEST/etc.
	// don't — they describe lifecycle changes, not actual usage. The
	// 5-state status taxonomy's Stale check (last_used_at < now-90d)
	// must reflect real usage to be meaningful.
	if event == AuditEventUse {
		if err := pushLastUsedIP(ctx, tx, credentialID, ip, now); err != nil {
			return err
		}
	}

	return nil
}

// RecordCredentialEvent appends one row to credential_audit and, when
// event is USE, also refreshes the denormalised last_used_at +
// last_used_ips on credentials.
//
// Callers provide the full context. ip is optional — empty string is
// stored as NULL. metadata is optional — pass nil for events whose
// type alone is sufficient.
//
// The whole operation runs in a single transaction so the row-level
// snapshot can never drift from the timeline (e.g. last_used_at
// pointing at an event that wasn't actually persisted). Mutation
// handlers that already run in their own transaction should call
// RecordCredentialEventTx inside it instead, so the audit row commits
// or rolls back together with the mutation.
func RecordCredentialEvent(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	credentialID string,
	event CredentialAuditEvent,
	agentID string,
	ip string,
	metadata map[string]any,
) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && logger != nil {
			logger.Warn("audit tx rollback", "error", rbErr)
		}
	}()

	if err := RecordCredentialEventTx(ctx, tx, credentialID, event, agentID, ip, metadata); err != nil {
		return err
	}

	return tx.Commit()
}

// recordCredentialEventBestEffort persists an audit event for a
// mutation that has ALREADY committed (no surrounding tx to ride). A
// failure never propagates to the caller — the mutation is done and
// must not be reported as failed — but it is counted in
// credentialAuditDropped and logged under the stable event name
// "credential_audit_write_failed" so operators can alarm on lost
// compliance events instead of grepping for free-form Warn lines.
func recordCredentialEventBestEffort(
	ctx context.Context,
	db *sql.DB,
	logger *slog.Logger,
	credentialID string,
	event CredentialAuditEvent,
	agentID string,
	ip string,
	metadata map[string]any,
) {
	if err := RecordCredentialEvent(ctx, db, logger, credentialID, event, agentID, ip, metadata); err != nil {
		credentialAuditDropped.Add(1)
		if logger != nil {
			logger.Warn("credential_audit_write_failed",
				"event", string(event),
				"credential_id", credentialID,
				"error", err,
			)
		}
	}
}

// pushLastUsedIP updates credentials.last_used_at and pushes the IP
// onto the front of last_used_ips, capping the ringbuffer at
// lastUsedIPRingSize. A repeat IP is moved to the front (so the list
// always reads "5 most recent unique IPs in order"). NULL/empty IPs
// are skipped — last_used_at still updates so the Stale check works.
func pushLastUsedIP(ctx context.Context, tx auditExecer, credentialID, ip, now string) error {
	if ip == "" {
		_, err := tx.ExecContext(ctx, `UPDATE credentials SET last_used_at = ? WHERE id = ?`, now, credentialID)
		return err
	}

	var existing sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT last_used_ips FROM credentials WHERE id = ?`, credentialID).Scan(&existing); err != nil {
		return fmt.Errorf("read last_used_ips: %w", err)
	}

	ips := []string{}
	if existing.Valid && strings.TrimSpace(existing.String) != "" {
		_ = json.Unmarshal([]byte(existing.String), &ips)
	}

	// Move-to-front semantics: drop any prior occurrence of this IP,
	// prepend, then truncate to the cap.
	out := []string{ip}
	for _, prev := range ips {
		if prev == ip {
			continue
		}
		out = append(out, prev)
		if len(out) >= lastUsedIPRingSize {
			break
		}
	}

	raw, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal last_used_ips: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE credentials SET last_used_at = ?, last_used_ips = ? WHERE id = ?`,
		now, string(raw), credentialID)
	return err
}

// parseLastUsedIPs unmarshals the credentials.last_used_ips TEXT
// column (JSON array) into a Go slice. Defensive against malformed
// JSON — returns []string{} so the response field is always present
// as an array, never null. Used by both List and Get handlers.
func parseLastUsedIPs(raw sql.NullString) []string {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return []string{}
	}
	var ips []string
	if err := json.Unmarshal([]byte(raw.String), &ips); err != nil {
		return []string{}
	}
	return ips
}

// parseTags unmarshals credentials.tags into a slice. NULL/empty/
// malformed all collapse to []string{} so callers don't have to
// branch.
func parseTags(raw sql.NullString) []string {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return []string{}
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw.String), &tags); err != nil {
		return []string{}
	}
	return tags
}

// normaliseTags trims, lowercases, dedupes, and caps the tag list to
// keep storage predictable. Empty input → nil so the column stays
// NULL rather than "[]"-as-text.
func normaliseTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(strings.ToLower(t))
		if t == "" || len(t) > 32 {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// encodeTagsJSON serialises a slice for the credentials.tags column.
// Returns ("", false) for empty input so callers can write SQL NULL.
func encodeTagsJSON(tags []string) (string, bool) {
	tags = normaliseTags(tags)
	if len(tags) == 0 {
		return "", false
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// auditEventResponse is the shape returned by the audit list endpoint.
// metadata_json is exposed as a parsed object so the FE doesn't need
// to second-parse the embedded JSON string.
type auditEventResponse struct {
	ID         string         `json:"id"`
	EventType  string         `json:"event_type"`
	AgentID    *string        `json:"agent_id"`
	IPAddress  *string        `json:"ip_address"`
	Metadata   map[string]any `json:"metadata"`
	OccurredAt string         `json:"occurred_at"`

	// Who did it, resolved once here rather than guessed per surface.
	//
	// The actor was recorded in two different shapes and, within metadata,
	// under five different keys — `agent_id` for a sidecar read, then
	// `revealed_by` / `rotated_by` / `created_by` / `approved_by` /
	// `rejected_by` for the human paths. A console reading the timeline could
	// therefore say WHAT happened and not WHO, which is the first question
	// asked of an audit log and the whole point of keeping one.
	//
	// ActorKind is "agent", "user", "crew" or "system". "crew" is a sidecar
	// fetch, which serves a container rather than one agent; "system" is the
	// honest answer for a row nobody signed, not a placeholder for a lookup we
	// skipped.
	ActorKind string `json:"actor_kind"`
	// ActorID is the agent id or the user id. Empty for "system", and it is
	// what an avatar is keyed by.
	ActorID string `json:"actor_id"`
	// ActorName is the agent's name or the user's full name (falling back to
	// their email). Empty when the actor is known by id but no longer exists —
	// a deleted agent still did the thing, so the row keeps its id.
	ActorName string `json:"actor_name"`
}

// auditActorMetadataKeys are the metadata keys that have been used to record
// the human behind an event, newest convention first.
//
// One list, in one place, because the alternative is every reader inventing
// its own — and a reader that has not heard of `rejected_by` silently reports
// a rejection as unattributed.
var auditActorMetadataKeys = []string{
	"revealed_by", "rotated_by", "created_by", "approved_by",
	"rejected_by", "updated_by", "deleted_by", "actor_id",
}

// resolveAuditActor decides who an event belongs to.
//
// Agent wins over metadata: the column is a foreign key the database enforces,
// while the metadata keys are free-form and, on an agent-driven path, may
// carry the operator who set the automation up rather than the actor.
func resolveAuditActor(agentID *string, metadata map[string]any) (kind, id string) {
	if agentID != nil && *agentID != "" {
		return "agent", *agentID
	}
	for _, key := range auditActorMetadataKeys {
		if v, ok := metadata[key].(string); ok && v != "" {
			return "user", v
		}
	}
	// A sidecar fetch has no agent — it serves a whole container — but it does
	// have the crew that owns it, and "the platform crew's container read this"
	// is an answer where "system" is only an admission.
	//
	// Gated on the marker, not on crew_id alone: plenty of events could carry a
	// crew id as SCOPE rather than as actor, and attributing one of those to a
	// crew would be a confident wrong answer where "system" was merely a dull
	// right one.
	if src, _ := metadata["source"].(string); src == "sidecar_fetch" {
		if v, ok := metadata["crew_id"].(string); ok && v != "" {
			return "crew", v
		}
	}
	return "system", ""
}

// AuditTimeline returns the most recent N audit events for a single
// credential. Backs the Audit tab in the detail Sheet.
//
// GET /api/v1/credentials/{credentialId}/audit?limit=50
func (h *CredentialHandler) AuditTimeline(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	credentialID := r.PathValue("credentialId")
	role := RoleFromContext(r.Context())

	// Audit reveals IPs of admin actions (rotate, test, revoke) — that's
	// forensic data, not for VIEWER/MEMBER eyes. Anyone who can update
	// credentials can read their audit; below that, 403.
	if !canRole(role, "update") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	// Workspace isolation: a missing or cross-workspace credential
	// must 404 the same way the rest of the credential handlers do,
	// rather than leak existence via a 200 with empty timeline.
	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT id FROM credentials
		WHERE id = ? AND workspace_id = ?`, // audit is forensic — must survive soft-delete (REVOKE is written after deleted_at is set)
		credentialID, workspaceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Credential not found")
			return
		}
		replyInternalError(w, h.logger, "audit: check credential exists", err)
		return
	}

	limit := auditDefaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		var n int
		if _, err := fmt.Sscanf(l, "%d", &n); err == nil && n > 0 && n <= auditMaxLimit {
			limit = n
		}
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, event_type, agent_id, ip_address, metadata_json, occurred_at
		FROM credential_audit
		WHERE credential_id = ?
		ORDER BY occurred_at DESC, id DESC
		LIMIT ?`, credentialID, limit)
	if err != nil {
		replyInternalError(w, h.logger, "query credential audit", err)
		return
	}
	defer rows.Close()

	out := []auditEventResponse{}
	for rows.Next() {
		var e auditEventResponse
		var rawMeta sql.NullString
		if err := rows.Scan(&e.ID, &e.EventType, &e.AgentID, &e.IPAddress, &rawMeta, &e.OccurredAt); err != nil {
			h.logger.Error("scan credential audit", "error", err)
			continue
		}
		if rawMeta.Valid && rawMeta.String != "" {
			_ = json.Unmarshal([]byte(rawMeta.String), &e.Metadata)
		}
		e.ActorKind, e.ActorID = resolveAuditActor(e.AgentID, e.Metadata)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (credential audit)", err)
		return
	}
	h.nameAuditActors(r.Context(), out)

	writeJSON(w, http.StatusOK, out)
}

// nameAuditActors fills ActorName in place, one query per actor kind for the
// whole page rather than one per row.
//
// A name that cannot be resolved is left empty rather than replaced with the
// id: the id is already on the row, and printing it twice under a heading that
// says "name" is how a console starts asserting that somebody is called
// "cmslzgmw9000eaf679312". The caller decides what to show instead.
func (h *CredentialHandler) nameAuditActors(ctx context.Context, events []auditEventResponse) {
	agentIDs := map[string]struct{}{}
	userIDs := map[string]struct{}{}
	crewIDs := map[string]struct{}{}
	for _, e := range events {
		switch e.ActorKind {
		case "agent":
			agentIDs[e.ActorID] = struct{}{}
		case "user":
			userIDs[e.ActorID] = struct{}{}
		case "crew":
			crewIDs[e.ActorID] = struct{}{}
		}
	}

	names := map[string]string{}
	h.collectNames(ctx, names, agentIDs,
		"SELECT id, name FROM agents WHERE id IN (%s)")
	h.collectNames(ctx, names, userIDs,
		"SELECT id, COALESCE(NULLIF(full_name, ''), email) FROM users WHERE id IN (%s)")
	h.collectNames(ctx, names, crewIDs,
		"SELECT id, name FROM crews WHERE id IN (%s)")

	for i := range events {
		if n, ok := names[events[i].ActorID]; ok {
			events[i].ActorName = n
		}
	}
}

// collectNames runs one id → name lookup and merges it into `into`. A failed
// query leaves the names unresolved, which the timeline renders as an
// unattributed row — never as a wrong attribution.
func (h *CredentialHandler) collectNames(
	ctx context.Context, into map[string]string, ids map[string]struct{}, queryFmt string,
) {
	if len(ids) == 0 {
		return
	}
	args := make([]any, 0, len(ids))
	for id := range ids {
		args = append(args, id)
	}
	rows, err := h.db.QueryContext(ctx, fmt.Sprintf(queryFmt, sqlPlaceholders(len(args))), args...)
	if err != nil {
		h.logger.Error("audit: resolve actor names", "error", err, "n", len(args))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			h.logger.Error("audit: scan actor name", "error", err)
			continue
		}
		if name.Valid && name.String != "" {
			into[id] = name.String
		}
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("audit: iterate actor names", "error", err)
	}
}
