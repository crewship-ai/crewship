package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// UserPreferencesHandler — generic key-value store for per-user UI
// settings (panel sizes, last-opened tabs, density, …). Values are
// stored as raw JSON strings in user_preferences.pref_value (migration
// v58) so the FE owns the schema per key. The handler intentionally
// does not validate value shape: callers know what they wrote.
//
//	GET    /api/v1/me/preferences            — full map { key: parsed-value }
//	PUT    /api/v1/me/preferences/{key}      — body is raw JSON value
//	DELETE /api/v1/me/preferences/{key}      — drops the row
//
// Keys must match [a-zA-Z0-9._-]{1,64} (defensive — values land in
// query-param paths and JSON keys; reject obvious abuse early).
type UserPreferencesHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewUserPreferencesHandler(db *sql.DB, logger *slog.Logger) *UserPreferencesHandler {
	return &UserPreferencesHandler{db: db, logger: logger}
}

func (h *UserPreferencesHandler) requireUser(r *http.Request) (string, bool) {
	u := UserFromContext(r.Context())
	if u == nil || u.ID == "" {
		return "", false
	}
	return u.ID, true
}

func validPrefKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, r := range k {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (h *UserPreferencesHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	rows, err := h.db.QueryContext(r.Context(),
		"SELECT pref_key, pref_value FROM user_preferences WHERE user_id = ?", userID)
	if err != nil {
		h.logger.Error("list user preferences", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	defer rows.Close()
	out := make(map[string]json.RawMessage)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		// Round-trip via RawMessage so the FE receives parsed JSON
		// (number / object / array) rather than a JSON-encoded string.
		out[k] = json.RawMessage(v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *UserPreferencesHandler) Set(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	key := r.PathValue("key")
	if !validPrefKey(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid key"})
		return
	}
	// Read up to 16 KB — preferences are small UI settings, not blobs.
	// MaxBytesReader returns *http.MaxBytesError once the cap is hit;
	// we surface that as 413 instead of silently persisting a truncated
	// (but still parseable) JSON payload.
	limited := http.MaxBytesReader(w, r.Body, 16*1024)
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "value too large (max 16 KB)"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	value := string(body)
	if value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty value"})
		return
	}
	// Validate the body is parseable JSON — protects callers reading
	// the value back later.
	if !json.Valid([]byte(value)) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "value must be valid JSON"})
		return
	}
	id := generateCUID()
	_, err = h.db.ExecContext(r.Context(),
		`INSERT INTO user_preferences (id, user_id, pref_key, pref_value, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'))
		 ON CONFLICT(user_id, pref_key) DO UPDATE SET
		   pref_value = excluded.pref_value,
		   updated_at = excluded.updated_at`,
		id, userID, key, value)
	if err != nil {
		h.logger.Error("set user preference", "err", err, "key", key)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *UserPreferencesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	key := r.PathValue("key")
	if !validPrefKey(key) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid key"})
		return
	}
	_, err := h.db.ExecContext(r.Context(),
		"DELETE FROM user_preferences WHERE user_id = ? AND pref_key = ?", userID, key)
	if err != nil {
		h.logger.Error("delete user preference", "err", err, "key", key)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
