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
)

var validAuditEvents = map[CredentialAuditEvent]struct{}{
	AuditEventUse:      {},
	AuditEventRotate:   {},
	AuditEventTest:     {},
	AuditEventRevoke:   {},
	AuditEventDetected: {},
	AuditEventCreated:  {},
}

// RecordCredentialEvent appends one row to credential_audit and, when
// event is USE, also refreshes the denormalised last_used_at +
// last_used_ips on credentials.
//
// Callers (today: rotation + test handlers; tomorrow: sidecar event
// stream) provide the full context. ip is optional — empty string is
// stored as NULL. metadata is optional — pass nil for events whose
// type alone is sufficient.
//
// The whole operation runs in a single transaction so the row-level
// snapshot can never drift from the timeline (e.g. last_used_at
// pointing at an event that wasn't actually persisted).
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

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin audit tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && logger != nil {
			logger.Warn("audit tx rollback", "error", rbErr)
		}
	}()

	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credential_audit (id, credential_id, event_type, agent_id, ip_address, metadata_json, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		generateCUID(), credentialID, string(event), agentArg, ipArg, metaJSON, now); err != nil {
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

	return tx.Commit()
}

// pushLastUsedIP updates credentials.last_used_at and pushes the IP
// onto the front of last_used_ips, capping the ringbuffer at
// lastUsedIPRingSize. A repeat IP is moved to the front (so the list
// always reads "5 most recent unique IPs in order"). NULL/empty IPs
// are skipped — last_used_at still updates so the Stale check works.
func pushLastUsedIP(ctx context.Context, tx *sql.Tx, credentialID, ip, now string) error {
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
}

// AuditTimeline returns the most recent N audit events for a single
// credential. Backs the Audit tab in the detail Sheet.
//
// GET /api/v1/credentials/{credentialId}/audit?limit=50
func (h *CredentialHandler) AuditTimeline(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	credentialID := r.PathValue("credentialId")

	// Workspace isolation: a missing or cross-workspace credential
	// must 404 the same way the rest of the credential handlers do,
	// rather than leak existence via a 200 with empty timeline.
	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT id FROM credentials
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		credentialID, workspaceID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Credential not found"})
			return
		}
		h.logger.Error("audit: check credential exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		h.logger.Error("query credential audit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (credential audit)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, out)
}
