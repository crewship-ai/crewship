package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// InstanceSettingsHandler exposes CRUD over the singleton app_settings
// table (instance-global key/value store, see migration v88).
//
// Two RBAC tiers:
//
//   - read  (List, Get): OWNER / ADMIN / MANAGER of any workspace.
//     Sensitive values are redacted with `***`.
//   - write (Put, Delete): OWNER / ADMIN of any workspace. ADMIN-only
//     per SPEC-2 §10; OWNER inherits because the role hierarchy keeps
//     OWNER a strict superset of ADMIN powers everywhere else in the
//     codebase. See helpers.go::canRole("manage") for the same pairing.
//
// Sensitive prefixes are matched with strings.HasPrefix — anything
// under `smtp.password`, `oauth.*.client_secret`, or `webhook.*.secret`
// reads back as the redaction placeholder. Writes still go through
// (you can't read it back, but the value is stored on disk).
//
// Protected keys are bootstrap markers the manifest layer must never
// delete (otherwise re-running migrations would re-bootstrap a fresh
// instance with new IDs). Returns 403 + application/problem+json.
type InstanceSettingsHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewInstanceSettingsHandler wires the handler.
func NewInstanceSettingsHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *InstanceSettingsHandler {
	return &InstanceSettingsHandler{db: db, hub: hub, logger: logger}
}

// sensitivePrefixes is the prefix allowlist for redaction. Matched with
// strings.HasPrefix so e.g. `oauth.google.client_secret` is caught by
// the `oauth.` prefix combined with the `.client_secret` suffix below.
//
// Per the task contract the rule is "any matching prefixes" against the
// three patterns below, with the wildcard segments expressed as a
// prefix+contains pair. We collapse that into a single HasPrefix on
// `smtp.password` (no wildcard), and for the wildcarded patterns we
// verify the leading namespace prefix AND the trailing fragment.
//
// Examples that redact:
//   - smtp.password
//   - smtp.password.legacy
//   - oauth.google.client_secret
//   - oauth.linear.client_secret
//   - webhook.github.secret
//   - webhook.linear.secret
//
// Examples that do NOT redact:
//   - smtp.host
//   - oauth.google.client_id
//   - webhook.linear.url
var sensitiveRedaction = "***"

// isSensitiveKey returns true if reads of `key` must be redacted.
//
// The contract specifies these prefix patterns:
//   - smtp.password
//   - oauth.*.client_secret
//   - webhook.*.secret
//
// The two wildcarded patterns are encoded as a leading namespace plus
// trailing suffix check, which is equivalent to the user-visible
// behaviour ("the second-segment value can be anything").
func isSensitiveKey(key string) bool {
	if strings.HasPrefix(key, "smtp.password") {
		return true
	}
	if strings.HasPrefix(key, "oauth.") && strings.HasSuffix(key, ".client_secret") {
		return true
	}
	if strings.HasPrefix(key, "webhook.") && strings.HasSuffix(key, ".secret") {
		return true
	}
	return false
}

// protectedKeys are bootstrap markers DELETE refuses to touch. Keep this
// in sync with the manifest's ApplyReplace skip-list in
// internal/manifest/kinds/instance_setting.go (when that file lands).
var protectedKeys = []string{
	"instance.bootstrap_at",
	"instance.first_user_id",
	"schema.version",
}

// isProtectedKey reports whether `key` is on the deletion-protected
// allowlist. Linear scan (3 entries) is intentionally simpler than a
// map.
func isProtectedKey(key string) bool {
	for _, k := range protectedKeys {
		if k == key {
			return true
		}
	}
	return false
}

// writeProblemContentType is a writeProblem variant that lets us set
// `application/problem+json` per RFC 7807 §3 — writeProblem proper sends
// `application/json` (it routes through writeJSON). Used by the
// protected-key path so operators can detect this specific failure
// class from the Content-Type header alone.
func writeProblemContentType(w http.ResponseWriter, r *http.Request, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"type":     "about:blank",
		"title":    http.StatusText(status),
		"status":   status,
		"detail":   detail,
		"instance": r.URL.Path,
	})
}

// instanceSetting is the wire shape for one key/value entry.
type instanceSetting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ── 1. List — GET /api/v1/instance/settings ───────────────────────────

// List returns every app_settings row, with sensitive values redacted.
// RBAC: OWNER / ADMIN / MANAGER of the current workspace (the "create"
// tier in helpers.go::canRole).
func (h *InstanceSettingsHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT key, value, COALESCE(updated_at, '') FROM app_settings ORDER BY key ASC`)
	if err != nil {
		h.logger.Error("list app_settings", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	out := []instanceSetting{}
	for rows.Next() {
		var s instanceSetting
		if err := rows.Scan(&s.Key, &s.Value, &s.UpdatedAt); err != nil {
			h.logger.Error("scan app_setting", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if isSensitiveKey(s.Key) {
			s.Value = sensitiveRedaction
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (app_settings)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, out)
}

// ── 2. Get — GET /api/v1/instance/settings/{key} ──────────────────────

// Get returns one key/value pair, redacting the value if sensitive.
// RBAC: same read tier as List.
func (h *InstanceSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key is required")
		return
	}

	var s instanceSetting
	err := h.db.QueryRowContext(r.Context(),
		`SELECT key, value, COALESCE(updated_at, '') FROM app_settings WHERE key = ?`, key).
		Scan(&s.Key, &s.Value, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Setting not found")
			return
		}
		h.logger.Error("get app_setting", "error", err, "key", key)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if isSensitiveKey(s.Key) {
		s.Value = sensitiveRedaction
	}
	writeJSON(w, http.StatusOK, s)
}

// ── 3. Put — PUT /api/v1/instance/settings/{key} ──────────────────────

// Put upserts a value for `key`. RBAC: OWNER / ADMIN (the "manage" tier
// in helpers.go::canRole). The trigger trg_app_settings_touch_updated_at
// keeps updated_at fresh on UPDATE; on INSERT we let the column DEFAULT
// (`datetime('now')`) fire.
func (h *InstanceSettingsHandler) Put(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key is required")
		return
	}

	var req struct {
		Value *string `json:"value"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Value == nil {
		// Sentinel: distinguish "field absent" from "empty string". Empty
		// string IS a valid setting value (e.g. clearing a banner) so we
		// only reject missing-value, not "".
		writeProblem(w, r, http.StatusBadRequest, "value is required")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// INSERT … ON CONFLICT(key) DO UPDATE — both branches set updated_at
	// explicitly so the row's stamp reflects the API write, not the DDL
	// default (which would be the original insert time on a no-op update).
	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, *req.Value, now)
	if err != nil {
		h.logger.Error("upsert app_setting", "error", err, "key", key)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Echo back the stored row with redaction applied. The client gets
	// `***` back even on the same request that set the value — matches
	// "write-only on read" semantics from SPEC-2 §10.
	resp := instanceSetting{Key: key, Value: *req.Value, UpdatedAt: now}
	if isSensitiveKey(key) {
		resp.Value = sensitiveRedaction
	}

	// Broadcast so workspace admins see instance-level config drift in
	// real time without polling. Sensitive values are NOT included in
	// the payload — only the key, so listeners can re-fetch and pick up
	// the redaction.
	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()),
		"instance_setting.updated", map[string]string{"key": key})

	writeJSON(w, http.StatusOK, resp)
}

// ── 4. Delete — DELETE /api/v1/instance/settings/{key} ────────────────

// Delete removes a key. Returns 403 + application/problem+json if the
// key is on the protected allowlist (bootstrap markers — see
// protectedKeys above). RBAC: OWNER / ADMIN ("manage").
func (h *InstanceSettingsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		writeProblem(w, r, http.StatusBadRequest, "key is required")
		return
	}

	// Protected-key guard fires BEFORE the DELETE — we want to return
	// 403 even if the row doesn't exist, so a probing client can't tell
	// whether `instance.bootstrap_at` was ever set.
	if isProtectedKey(key) {
		writeProblemContentType(w, r, http.StatusForbidden,
			"Cannot delete protected setting: "+key)
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM app_settings WHERE key = ?`, key)
	if err != nil {
		h.logger.Error("delete app_setting", "error", err, "key", key)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete app_setting rows affected", "error", err, "key", key)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Setting not found")
		return
	}

	broadcastWorkspaceEvent(h.hub, WorkspaceIDFromContext(r.Context()),
		"instance_setting.deleted", map[string]string{"key": key})

	w.WriteHeader(http.StatusNoContent)
}
