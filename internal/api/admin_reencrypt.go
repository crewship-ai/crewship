package api

// POST /api/v1/admin/reencrypt — re-encrypt every stored AES-256-GCM
// envelope to the CURRENT master-key version (master-key rotation, E1).
//
// Why this exists: internal/encryption supports versioned envelopes
// ("v1:…", "v2:…") and can DECRYPT any generation whose key is still in the
// environment, but before this endpoint there was no way to move rows
// forward — rotating the master key meant carrying the old key env var
// forever. This handler walks every inventoried envelope column, decrypts
// with whatever key version the envelope names, re-encrypts with the current
// version, and reports counts. After a run that reports failed=0 the old
// key can be retired.
//
// The column inventory below is the single source of truth for "where do
// envelopes live". If you add a new encryption.Encrypt call site that
// persists to a NEW column, add it here in the same PR — otherwise key
// rotation silently strands your column on the old key.
//
// Instance-wide on purpose: the master key is per-instance, not
// per-workspace, so the walk covers ALL workspaces. The route still
// registers through the standard admin chain (authedMut → RequireWorkspace +
// roleManage) — same model as /admin/backups and /admin/prune-legacy-
// resources, which are also instance-scoped operations gated on the caller
// being ADMIN+ somewhere.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// reencryptTarget names one envelope-bearing column. Table/Column/Where are
// compile-time constants assembled into SQL — never caller input.
type reencryptTarget struct {
	Table  string
	Column string
	// Where is an extra predicate for columns that only SOMETIMES hold
	// envelopes (e.g. escalations.resolution is plaintext for non-CREDENTIAL
	// types — updating those would corrupt operator text).
	Where string
	// FailOpen marks a column that is encrypted at rest only when a key is
	// configured (webhook secrets, #1072). A bare (non-enveloped) value there
	// is EXPECTED legacy/key-less state, not a rotation failure — so it counts
	// as Skipped rather than Failed, keeping the "failed=0 ⇒ retire old key"
	// signal honest.
	FailOpen bool
}

// reencryptTargets is the exhaustive envelope-column inventory (verified
// against every encryption.Encrypt call site):
//
//   - credentials.encrypted_value          — main secret / access token
//   - credentials.encrypted_refresh_token  — legacy refresh-token column (v01)
//   - credentials.oauth_client_secret_enc  — OAuth2 client secret
//   - credentials.oauth_refresh_token_enc  — OAuth2 refresh token (v26+)
//   - credential_rotations.old_value       — previous envelope, grace window
//   - notification_channels.secret_enc     — webhook HMAC signing secret
//   - composio_settings.encrypted_api_key  — Composio API key
//   - oauth_states.code_verifier           — PKCE verifier (ephemeral rows)
//   - escalations.resolution               — ONLY WHERE type='CREDENTIAL'
//   - agents.webhook_secret                — agent webhook signing secret (#1072/#1029)
//   - pipeline_webhooks.signing_secret     — pipeline webhook HMAC key (#1029)
//
// The two webhook columns are FAIL-OPEN at rest (encrypted only when a key is
// configured; #1072). A key-less deployment can't run reencrypt at all, and a
// key-ful one has these enveloped by migration v140 — so any bare row a
// rotation encounters is left untouched (reencryptColumn's undecryptable path),
// never corrupted.
//
// Non-SQLite envelope storage (~/.crewship/backup-keyring.enc) is handled
// separately; see the runbook in docs/guides/credentials.mdx.
var reencryptTargets = []reencryptTarget{
	{Table: "credentials", Column: "encrypted_value"},
	{Table: "credentials", Column: "encrypted_refresh_token"},
	{Table: "credentials", Column: "oauth_client_secret_enc"},
	{Table: "credentials", Column: "oauth_refresh_token_enc"},
	{Table: "credential_rotations", Column: "old_value"},
	{Table: "notification_channels", Column: "secret_enc"},
	{Table: "composio_settings", Column: "encrypted_api_key"},
	{Table: "oauth_states", Column: "code_verifier"},
	{Table: "escalations", Column: "resolution", Where: "type = 'CREDENTIAL'"},
	{Table: "agents", Column: "webhook_secret", FailOpen: true},
	{Table: "pipeline_webhooks", Column: "signing_secret", FailOpen: true},
}

// reencryptBatchSize bounds how many row UPDATEs share one transaction:
// small enough to keep writer-lock hold times negligible for concurrent
// request traffic, large enough that a big credentials table doesn't pay a
// per-row fsync.
const reencryptBatchSize = 200

type reencryptColumnResult struct {
	Table       string `json:"table"`
	Column      string `json:"column"`
	Reencrypted int    `json:"reencrypted"`
	Skipped     int    `json:"skipped"`
	Failed      int    `json:"failed"`
}

type reencryptResponse struct {
	KeyVersion  string                  `json:"key_version"`
	Reencrypted int                     `json:"reencrypted"`
	Skipped     int                     `json:"skipped"`
	Failed      int                     `json:"failed"`
	Columns     []reencryptColumnResult `json:"columns"`
}

// ReencryptHandler owns POST /api/v1/admin/reencrypt.
type ReencryptHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewReencryptHandler(db *sql.DB, logger *slog.Logger) *ReencryptHandler {
	return &ReencryptHandler{db: db, logger: logger}
}

// Reencrypt walks the envelope inventory and re-encrypts every value to the
// current key version. Idempotent: envelopes already at the current version
// are skipped, so re-running after a partial failure only touches the
// remainder. Counts land in the response; plaintext never lands anywhere —
// not in logs, not in errors, not in the audit row.
func (h *ReencryptHandler) Reencrypt(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !canRole(RoleFromContext(ctx), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if WorkspaceIDFromContext(ctx) == "" {
		replyError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	// Fail closed BEFORE touching any row: a misconfigured rotation
	// (CREWSHIP_ENCRYPTION_KEY_VERSION=v2 without ENCRYPTION_KEY_V2, bad hex,
	// wrong length) must abort with zero writes, not mint broken envelopes.
	version, err := encryption.CurrentKeyVersion()
	if err == nil {
		err = encryption.VerifyCurrentKey()
	}
	if err != nil {
		h.logger.Error("reencrypt aborted: key configuration invalid", "error", err)
		replyError(w, http.StatusInternalServerError, fmt.Sprintf("re-encryption aborted: %v", err))
		return
	}

	resp := reencryptResponse{KeyVersion: version, Columns: make([]reencryptColumnResult, 0, len(reencryptTargets))}
	for _, tgt := range reencryptTargets {
		res, err := h.reencryptColumn(ctx, tgt, version)
		resp.Columns = append(resp.Columns, res)
		resp.Reencrypted += res.Reencrypted
		resp.Skipped += res.Skipped
		resp.Failed += res.Failed
		if err != nil {
			// Infrastructure failure (query/tx error) mid-walk. Return the
			// partial counts with the 500 — the operator drives this from the
			// CLI and needs to see how far it got; a re-run is safe because
			// completed columns skip.
			h.logger.Error("reencrypt failed", "table", tgt.Table, "column", tgt.Column, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":       fmt.Sprintf("re-encryption failed on %s.%s: %v", tgt.Table, tgt.Column, err),
				"key_version": resp.KeyVersion,
				"reencrypted": resp.Reencrypted,
				"skipped":     resp.Skipped,
				"failed":      resp.Failed,
				"columns":     resp.Columns,
			})
			return
		}
	}

	userID := ""
	if u := UserFromContext(ctx); u != nil {
		userID = u.ID
	}
	WriteAuditLog(ctx, h.db, nil, "admin.reencrypt", "encryption_key", version, userID, WorkspaceIDFromContext(ctx), map[string]any{
		"key_version": version,
		"reencrypted": resp.Reencrypted,
		"skipped":     resp.Skipped,
		"failed":      resp.Failed,
	})
	h.logger.Info("reencrypt complete",
		"key_version", version,
		"reencrypted", resp.Reencrypted,
		"skipped", resp.Skipped,
		"failed", resp.Failed)
	writeJSON(w, http.StatusOK, resp)
}

// reencryptColumn processes one table.column: decrypt (any key version the
// envelope names), re-encrypt with the current version, UPDATE in batched
// transactions. Per-row decrypt failures are counted and logged WITHOUT the
// value (unknown key, corrupt data — the row is left untouched); encrypt
// failures abort (the current key just verified, so one failing means the
// environment changed under us).
func (h *ReencryptHandler) reencryptColumn(ctx context.Context, tgt reencryptTarget, version string) (reencryptColumnResult, error) {
	res := reencryptColumnResult{Table: tgt.Table, Column: tgt.Column}

	// NULL and '' are "no secret stored" (PENDING OAuth rows, scrubbed
	// rotation rows, email channels) — excluded here, so they count as
	// neither skipped nor failed.
	query := fmt.Sprintf(`SELECT rowid, %s FROM %s WHERE %s IS NOT NULL AND %s != ''`,
		tgt.Column, tgt.Table, tgt.Column, tgt.Column)
	if tgt.Where != "" {
		query += " AND " + tgt.Where
	}

	rows, err := h.db.QueryContext(ctx, query)
	if err != nil {
		return res, fmt.Errorf("select: %w", err)
	}
	type pendingUpdate struct {
		rowid    int64
		oldValue string
		newValue string
	}
	var updates []pendingUpdate
	for rows.Next() {
		var rowid int64
		var value string
		if err := rows.Scan(&rowid, &value); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan: %w", err)
		}
		if v, ok := encryption.ParseEnvelopeVersion(value); ok && v == version {
			res.Skipped++
			continue
		}
		// Fail-open columns (webhook secrets): a bare, non-enveloped value is
		// the EXPECTED key-less/legacy state, not a rotation failure — count it
		// as Skipped so it doesn't poison the "failed=0 ⇒ retire old key" gate.
		if tgt.FailOpen && !encryption.IsEncrypted(value) {
			res.Skipped++
			continue
		}
		// Older envelope or legacy raw-base64 value: decrypt with whatever
		// key its prefix names (legacy = v1), re-encrypt with the current key.
		plain, err := encryption.Decrypt(value)
		if err != nil {
			// Row id + shape only — never the value (it may be a plaintext
			// secret that predates encryption).
			h.logger.Warn("reencrypt: undecryptable value left untouched",
				"table", tgt.Table, "column", tgt.Column, "rowid", rowid)
			res.Failed++
			continue
		}
		enc, err := encryption.Encrypt(plain)
		if err != nil {
			rows.Close()
			return res, fmt.Errorf("encrypt with %s key: %w", version, err)
		}
		updates = append(updates, pendingUpdate{rowid: rowid, oldValue: value, newValue: enc})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return res, fmt.Errorf("iterate: %w", err)
	}
	rows.Close()

	// Guarded UPDATE (… AND column = old) so a row rewritten concurrently
	// (credential updated mid-run, ephemeral oauth_states row consumed) is
	// left to its writer and counted skipped — its new value was already
	// minted at the current version.
	stmt := fmt.Sprintf(`UPDATE %s SET %s = ? WHERE rowid = ? AND %s = ?`, tgt.Table, tgt.Column, tgt.Column)
	for start := 0; start < len(updates); start += reencryptBatchSize {
		end := min(start+reencryptBatchSize, len(updates))
		tx, err := h.db.BeginTx(ctx, nil)
		if err != nil {
			return res, fmt.Errorf("begin tx: %w", err)
		}
		for _, u := range updates[start:end] {
			r, err := tx.ExecContext(ctx, stmt, u.newValue, u.rowid, u.oldValue)
			if err != nil {
				tx.Rollback()
				return res, fmt.Errorf("update rowid %d: %w", u.rowid, err)
			}
			if n, _ := r.RowsAffected(); n == 0 {
				res.Skipped++
			} else {
				res.Reencrypted++
			}
		}
		if err := tx.Commit(); err != nil {
			return res, fmt.Errorf("commit: %w", err)
		}
	}
	return res, nil
}
