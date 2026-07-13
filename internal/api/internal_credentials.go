package api

import (
	"context"
	"database/sql"
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

	// USE debounce stays best-effort by design: an audit hiccup must
	// never fail (or slow) the sidecar credential fetch. The drop
	// counter + stable log event replace the old silent Warn.
	recordCredentialEventBestEffort(ctx, db, logger, credID, AuditEventUse, "" /* agent unknown at this layer */, ip, map[string]any{"source": "sidecar_fetch"})
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

	// Status gate. The metadata-only path (default sidecar/LEAD discovery) keeps
	// returning EXPIRED/ERROR so the management view can surface unhealthy
	// credentials. But the include_values path decrypts and hands back plaintext
	// tokens — that path is runtime credential INJECTION (the in-process LLM
	// proxy TokenSyncer), so a non-ACTIVE credential there is a revoked secret
	// being handed to an agent. When include_values is in effect we hard-filter
	// to status = 'ACTIVE' so an EXPIRED/ERROR/REVOKED token is never decrypted
	// and injected. The gate rides the exact same condition that already
	// controls value exposure, so the management view is unaffected.
	// RATE_LIMITED is included in the metadata set: the sidecar's credential
	// reaper reaps any boot-time credential NOT returned here, so excluding a
	// transiently RATE_LIMITED key would permanently evict a still-valid key
	// (until container restart). Only genuinely-gone credentials (REVOKED /
	// deleted_at) must be absent so the reaper drops exactly those.
	statusClause := "status IN ('ACTIVE', 'EXPIRED', 'ERROR', 'RATE_LIMITED')"
	if includeValues {
		statusClause = "status = 'ACTIVE'"
	}

	query := `SELECT id, workspace_id, name, type, provider, encrypted_value,
		encrypted_refresh_token, token_expires_at, account_label, account_email, status
		FROM credentials
		WHERE ` + statusClause + ` AND deleted_at IS NULL
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
	// #1031: when the caller identifies its crew, scope the metadata listing to
	// credentials that crew can actually use — assigned to one of the crew's
	// agents (agent_credentials), directly crew-scoped (credential_crews), or
	// workspace-scoped — so a compromised agent container can't enumerate every
	// peer credential's existence/provider through its sidecar. crew_id is
	// supplied server-side by the sidecar from its bound IPC config (the agent
	// can't forge it); an empty crew_id keeps the workspace-wide behaviour the
	// in-process TokenSyncer (include_values, loopback) and crew-less callers
	// rely on.
	//
	// scope = 'WORKSPACE' MUST be included in the OR: those credentials have
	// no agent_credentials/credential_crews row by definition (that's what
	// "workspace-wide" means — see credentialVisibilityFilter, the same idiom
	// on the public API), so without it a scope=WORKSPACE credential with no
	// assignment yet — e.g. one an agent just created via sidecar self-service
	// (default scope=WORKSPACE, no crew_ids) — would be invisible in its own
	// very next crew-scoped listing. A CREW-scoped credential belonging to a
	// DIFFERENT crew is still excluded (the actual #1031 leak this scoping
	// closes): it has neither an agent_credentials row pointing at THIS crew's
	// agents, nor a credential_crews row naming THIS crew, nor scope=WORKSPACE.
	if crewID := r.URL.Query().Get("crew_id"); crewID != "" {
		query += ` AND (
			credentials.scope = 'WORKSPACE'
			OR EXISTS (SELECT 1 FROM agent_credentials ac
			        JOIN agents a ON a.id = ac.agent_id
			        WHERE ac.credential_id = credentials.id
			          AND a.crew_id = ? AND a.deleted_at IS NULL)
			OR EXISTS (SELECT 1 FROM credential_crews cc
			           WHERE cc.credential_id = credentials.id AND cc.crew_id = ?)
		)`
		args = append(args, crewID, crewID)
	} else if !requestIsLoopback(r) {
		// Hardening (#1031): the crew scoping above is opt-in — a caller that
		// omits crew_id gets the full workspace-wide listing, fail-open. A
		// legitimate non-loopback caller always has a crew_id to send (the
		// sidecar attaches its own bound crew), so a non-loopback call
		// WITHOUT one is either an old/misconfigured sidecar or a bypass
		// attempt. Full closure needs crew-bound internal tokens (not just
		// crew_id, which any caller with a valid X-Internal-Token can forge);
		// until then, at least make the bypass visible in ops.
		h.logger.Warn("internal credentials: workspace-wide listing (no crew_id) from non-loopback caller",
			"remote_addr", r.RemoteAddr, "workspace_id", workspaceID)
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

	// Build WHERE clause — workspace_id is optional (internal callers may not send it).
	// deleted_at IS NULL (#1061): a status write (e.g. the OAuth refresh worker)
	// must not mutate a soft-deleted credential — without this it would flip a
	// dead row's status back to ACTIVE and bump updated_at, returning 200. Every
	// other credential mutation filters deleted_at; the n==0→404 below then
	// correctly rejects deleted credentials. Reused by the last_error / token
	// updates in this handler.
	where := "id = ? AND deleted_at IS NULL"
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

	// A REVOKED transition must remove the credential's materialized
	// /secrets file(s) from running crew containers, exactly like the public
	// DELETE handler — otherwise a sidecar-detected revocation leaves the
	// cleartext file readable until the next run. Async + bounded: the
	// sidecar's status PATCH must not stall on a docker exec, and a wedged
	// daemon must not hold the goroutine forever. Best-effort by design (the
	// DB status is already REVOKED, so the file is never re-materialized).
	if body.Status == "REVOKED" {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		h.reconcileWG.Add(1)
		go func() {
			defer h.reconcileWG.Done()
			defer cancel()
			reconcileRevokedCredentialFiles(rctx, h.db, h.logger, h.container, credID, workspaceID)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": credID, "status": body.Status, "last_checked_at": now})
}

// NOTE: the internal GET .../agents/{agentId}/webhook-secret endpoint was
// REMOVED (#999). It returned the webhook signing secret in plaintext JSON
// to any internal-token caller; its only consumer was the webhook trigger
// handler's IPC resolver hop, and that handler now reads the secret from
// its local DB (crew-scoped) without the secret ever leaving the process.
// Obtaining a secret is rotation-only: POST /api/v1/agents/{agentId}/
// webhook-secret/rotate returns the newly minted value exactly once.
