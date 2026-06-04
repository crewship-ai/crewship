package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// sidecarUseDebounce throttles sidecar-driven USE events so the audit
// timeline doesn't fill with one row per poll cycle. The sidecar fetches
// credentials whenever an agent is about to consume them; a 60s window
// is long enough to merge bursts (one agent run usually fetches each
// credential once at start, maybe again on rotate) and short enough to
// catch independent runs.
const sidecarUseDebounce = 60 * time.Second

// maybeRecordSidecarUse emits an AuditEventUse for credID iff the most
// recent USE event for the same credential is older than
// sidecarUseDebounce. Errors are logged but never bubbled — audit
// failure must not block the credential fetch the sidecar is doing.
//
// Closes the gap noted in F-3 of the credentials/scrubber pentest agent:
// the audit timeline previously showed only manual Test/Rotate/Revoke,
// not the actual sidecar-driven USE that's what an incident responder
// most cares about ("when was this credential last touched?").
//
// The debounce check is implemented as an atomic compare-and-swap
// against the credentials.last_used_at column rather than a read-then-
// decide pattern. Pre-fix two parallel sidecar polls within the
// debounce window could both read the stale timestamp, both decide to
// record, and emit two audit rows for what should have been one event.
// CodeRabbit caught the race on the first review pass. The CAS
// guarantees exactly one goroutine wins per debounce window — the
// loser's UPDATE matches zero rows and bails before recording.
func maybeRecordSidecarUse(ctx context.Context, db *sql.DB, logger *slog.Logger, credID string, ip string) {
	if credID == "" {
		return
	}

	// CAS step: bump last_used_at iff the stored value is older than
	// (now - debounce) — or NULL (never used). A successful UPDATE means
	// we own the right to record. Concurrent callers see RowsAffected == 0
	// and bail.
	now := time.Now().UTC()
	threshold := now.Add(-sidecarUseDebounce).Format(time.RFC3339)
	nowRFC := now.Format(time.RFC3339)

	res, err := db.ExecContext(ctx, `
		UPDATE credentials
		   SET last_used_at = ?
		 WHERE id = ?
		   AND (last_used_at IS NULL OR last_used_at < ?)`,
		nowRFC, credID, threshold)
	if err != nil {
		// CAS failure (DB error, etc.) shouldn't block the fetch — log
		// and skip. Worst case the audit row is missed for this poll;
		// the next poll past the debounce will catch it.
		if logger != nil {
			logger.Debug("sidecar use audit: CAS update failed", "credential_id", credID, "error", err)
		}
		return
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		// Lost the race (another goroutine just bumped it) or the row
		// doesn't exist at all (already deleted). Either way: skip.
		return
	}

	if err := RecordCredentialEvent(ctx, db, logger, credID, AuditEventUse, "" /* agent unknown at this layer */, ip, map[string]any{"source": "sidecar_fetch"}); err != nil {
		if logger != nil {
			logger.Warn("sidecar use audit: record failed", "credential_id", credID, "error", err)
		}
	}
}

// ListCredentials returns active credentials for the sidecar.
// GET /api/v1/internal/credentials — called by sidecar to discover what's available.
//
// By default the response contains ONLY metadata (id, name, type, provider,
// status). Plaintext `access_token` / `refresh_token` are emitted ONLY when the
// caller passes `?include_values=true` AND the request arrived over a loopback
// connection (the in-process LLM proxy hairpin in internal/llmproxy uses this).
//
// Why the split: previously this endpoint always returned decrypted tokens,
// which meant any process inside an agent container could pull plaintext LLM
// credentials by calling the sidecar's `/credentials` proxy — the sidecar
// attaches X-Internal-Token automatically, so the agent rode the sidecar's
// trust straight to plaintext sk-ant-* / sk-* values. The sidecar already has
// the credentials it needs from its stdin boot payload (see
// orchestrator/exec_sidecar.go), so LEAD agents discovering credentials only
// need metadata. The opt-in keeps the LLM proxy's TokenSyncer working
// unchanged on the same endpoint.
func (h *InternalHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	provider := r.URL.Query().Get("provider")

	includeValues := r.URL.Query().Get("include_values") == "true"
	if includeValues && !requestIsLoopback(r) {
		h.logger.Warn("internal credentials: include_values=true rejected from non-loopback caller",
			"remote_addr", r.RemoteAddr, "workspace_id", workspaceID)
		includeValues = false
	}

	query := `SELECT id, workspace_id, name, type, provider, encrypted_value,
		encrypted_refresh_token, token_expires_at, account_label, account_email, status
		FROM credentials
		WHERE status IN ('ACTIVE', 'EXPIRED', 'ERROR') AND deleted_at IS NULL
		AND type IN ('AI_CLI_TOKEN', 'API_KEY') AND provider != 'NONE'`

	var args []any
	if workspaceID != "" {
		query += " AND workspace_id = ?"
		args = append(args, workspaceID)
	}
	if provider != "" {
		query += " AND provider = ?"
		args = append(args, provider)
	}
	query += " ORDER BY type ASC, created_at ASC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("internal list credentials", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	// Pointer fields so the JSON encoder omits them entirely when the caller
	// is not entitled to plaintext (default sidecar path). Empty-string would
	// still surface the key and risk a JS consumer treating "" as "present
	// but empty" instead of "withheld".
	type credResult struct {
		ID           string  `json:"id"`
		WorkspaceID  string  `json:"workspace_id"`
		Name         string  `json:"name"`
		Type         string  `json:"type"`
		Provider     string  `json:"provider"`
		AccessToken  *string `json:"access_token,omitempty"`
		RefreshToken *string `json:"refresh_token,omitempty"`
		TokenExpires *string `json:"token_expires_at"`
		AccountLabel *string `json:"account_label"`
		Status       string  `json:"status"`
	}

	var result []credResult
	for rows.Next() {
		var c credResult
		var encValue string
		var encRefresh, accountEmail sql.NullString
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Type, &c.Provider,
			&encValue, &encRefresh, &c.TokenExpires, &c.AccountLabel, &accountEmail, &c.Status); err != nil {
			h.logger.Error("scan internal credential", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if includeValues {
			decrypted, derr := encryption.Decrypt(encValue)
			if derr != nil {
				h.logger.Error("decrypt credential", "id", c.ID, "error", derr)
				continue
			}
			c.AccessToken = &decrypted
			if encRefresh.Valid {
				rt, rerr := encryption.Decrypt(encRefresh.String)
				if rerr != nil {
					h.logger.Debug("decrypt refresh token", "id", c.ID, "error", rerr)
				} else {
					c.RefreshToken = &rt
				}
			}
			// Best-effort USE audit. Empty IP is fine — internal callers are
			// the sidecar (loopback) and the IP would always be 127.0.0.1
			// which is no signal. Debounced to one row per credential per
			// minute so a busy sidecar doesn't flood the timeline.
			maybeRecordSidecarUse(r.Context(), h.db, h.logger, c.ID, "")
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (internal credentials)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if result == nil {
		result = []credResult{}
	}
	writeJSON(w, http.StatusOK, result)
}

// requestIsLoopback returns true when the request arrived from 127.0.0.0/8
// or ::1. Used by ListCredentials to gate the `include_values=true` opt-in to
// in-process callers (the LLM proxy TokenSyncer) only. Non-loopback callers
// (a sidecar reaching crewshipd via host.docker.internal, for example) get
// metadata-only regardless of the flag.
func requestIsLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// UpdateCredentialStatus updates the health status of a credential after the sidecar validates it.
// PATCH /api/v1/internal/credentials/{credentialId}/status
func (h *InternalHandler) UpdateCredentialStatus(w http.ResponseWriter, r *http.Request) {
	credID := r.PathValue("credentialId")
	workspaceID := r.URL.Query().Get("workspace_id") // optional for backwards compat

	var body struct {
		Status       string  `json:"status"`
		LastError    *string `json:"last_error"`
		AccessToken  *string `json:"access_token"`
		RefreshToken *string `json:"refresh_token"`
		TokenExpires *string `json:"token_expires_at"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	validStatuses := map[string]bool{
		"ACTIVE": true, "EXPIRED": true, "RATE_LIMITED": true, "REVOKED": true, "ERROR": true,
	}
	if !validStatuses[body.Status] {
		replyError(w, http.StatusBadRequest, "Invalid status")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Build WHERE clause — workspace_id is optional (internal callers may not send it)
	where := "id = ?"
	whereArgs := []any{credID}
	if workspaceID != "" {
		where += " AND workspace_id = ?"
		whereArgs = append(whereArgs, workspaceID)
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(r.Context(),
		"UPDATE credentials SET status = ?, last_checked_at = ?, updated_at = ? WHERE "+where,
		append([]any{body.Status, now, now}, whereArgs...)...)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		h.logger.Error("update credential rows affected", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n == 0 {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}

	if body.LastError != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET last_error = ? WHERE "+where, append([]any{*body.LastError}, whereArgs...)...); err != nil {
			h.logger.Error("update credential last_error", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if body.AccessToken != nil {
		enc, err := encryption.Encrypt(*body.AccessToken)
		if err != nil {
			h.logger.Error("encrypt access token", "error", err)
			replyError(w, http.StatusInternalServerError, "Failed to encrypt token")
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_value = ? WHERE "+where, append([]any{enc}, whereArgs...)...); err != nil {
			h.logger.Error("update credential access token", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if body.RefreshToken != nil {
		enc, err := encryption.Encrypt(*body.RefreshToken)
		if err != nil {
			h.logger.Error("encrypt refresh token", "error", err)
			replyError(w, http.StatusInternalServerError, "Failed to encrypt token")
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET encrypted_refresh_token = ? WHERE "+where, append([]any{enc}, whereArgs...)...); err != nil {
			h.logger.Error("update credential refresh token", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if body.TokenExpires != nil {
		if _, err := tx.ExecContext(r.Context(), "UPDATE credentials SET token_expires_at = ? WHERE "+where, append([]any{*body.TokenExpires}, whereArgs...)...); err != nil {
			h.logger.Error("update credential token_expires_at", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": credID, "status": body.Status, "last_checked_at": now})
}

// GetWebhookSecret returns the webhook secret for a given agent, used by the sidecar for signature validation.
// GET /api/v1/internal/agents/{agentId}/webhook-secret
func (h *InternalHandler) GetWebhookSecret(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")

	// Tenant scoping. The pre-fix query was `WHERE id = ?` with the agentID
	// straight from the path and no scoping, so any internal caller — or the
	// public webhook trigger flow, which lets the URL pick crew/agent — could
	// fetch ANY agent's webhook secret across workspace boundaries and then
	// forge a validly-signed webhook for that agent. We now constrain the
	// lookup by whatever tenant scope the caller supplies.
	//
	// Both scopes are OPTIONAL query params (same backwards-compat shape as
	// UpdateCredentialStatus above): the sidecar IPC resolver and the public
	// webhook handler reach this via SecretLookup(crewID, agentID) and pass
	// crew_id, while UI/admin callers pass workspace_id. A missing scope keeps
	// the legacy id-only behavior so existing internal callers don't break;
	// a present-but-mismatched scope yields the same 404 as a non-existent
	// agent (404 not 403 — don't leak that the agent exists in another tenant).
	query := "SELECT webhook_secret FROM agents WHERE id = ?"
	args := []any{agentID}
	if wsID := r.URL.Query().Get("workspace_id"); wsID != "" {
		query += " AND workspace_id = ?"
		args = append(args, wsID)
	}
	if crewID := r.URL.Query().Get("crew_id"); crewID != "" {
		query += " AND crew_id = ?"
		args = append(args, crewID)
	}

	var secret sql.NullString
	err := h.db.QueryRowContext(r.Context(), query, args...).Scan(&secret)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Agent not found")
			return
		}
		h.logger.Error("webhook secret lookup", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !secret.Valid || secret.String == "" {
		replyError(w, http.StatusNotFound, "Webhook secret not configured for agent")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"webhook_secret": secret.String})
}
