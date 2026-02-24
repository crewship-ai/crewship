package api

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const cliTokenPrefix = "crewship_cli_"

type CLITokenHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewCLITokenHandler(db *sql.DB, logger *slog.Logger) *CLITokenHandler {
	return &CLITokenHandler{db: db, logger: logger}
}

func (h *CLITokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var body struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		body.Name = "CLI token"
	}
	if body.Name == "" {
		body.Name = "CLI token"
	}

	// Generate random token: crewship_cli_ + 40 hex chars
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		h.logger.Error("generate token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	token := cliTokenPrefix + hex.EncodeToString(b)

	// Store SHA-256 hash
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(),
		"INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		id, user.ID, body.Name, tokenHash, now)
	if err != nil {
		h.logger.Error("insert cli_token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":      token,
		"id":         id,
		"name":       body.Name,
		"created_at": now,
	})
}

func (h *CLITokenHandler) Validate(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid":      true,
		"user_id":    user.ID,
		"user_email": user.Email,
	})
}

func (h *CLITokenHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		"SELECT id, name, created_at, last_used_at, revoked_at FROM cli_tokens WHERE user_id = ? ORDER BY created_at DESC", user.ID)
	if err != nil {
		h.logger.Error("list cli_tokens", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var tokens []map[string]interface{}
	for rows.Next() {
		var id, name, createdAt string
		var lastUsedAt, revokedAt sql.NullString
		if err := rows.Scan(&id, &name, &createdAt, &lastUsedAt, &revokedAt); err != nil {
			continue
		}
		t := map[string]interface{}{
			"id":         id,
			"name":       name,
			"created_at": createdAt,
		}
		if lastUsedAt.Valid {
			t["last_used_at"] = lastUsedAt.String
		}
		if revokedAt.Valid {
			t["revoked_at"] = revokedAt.String
		}
		tokens = append(tokens, t)
	}
	if tokens == nil {
		tokens = []map[string]interface{}{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"data": tokens})
}

func (h *CLITokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	tokenID := r.PathValue("tokenId")
	now := time.Now().UTC().Format(time.RFC3339)

	result, err := h.db.ExecContext(r.Context(),
		"UPDATE cli_tokens SET revoked_at = ? WHERE id = ? AND user_id = ?",
		now, tokenID, user.ID)
	if err != nil {
		h.logger.Error("revoke cli_token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Token not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// IsCLIToken returns true if the token has the CLI token prefix.
func IsCLIToken(token string) bool {
	return strings.HasPrefix(token, cliTokenPrefix)
}

// ValidateCLIToken validates a CLI token against the database.
// Returns (userID, error).
func ValidateCLIToken(db *sql.DB, token string) (string, string, string, error) {
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])

	var userID, email, name string
	var revokedAt sql.NullString
	err := db.QueryRow(`
		SELECT ct.user_id, u.email, u.full_name, ct.revoked_at
		FROM cli_tokens ct
		JOIN users u ON u.id = ct.user_id
		WHERE ct.token_hash = ?
	`, tokenHash).Scan(&userID, &email, &name, &revokedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", "", "", fmt.Errorf("invalid CLI token")
		}
		return "", "", "", fmt.Errorf("validate CLI token: %w", err)
	}

	if revokedAt.Valid {
		return "", "", "", fmt.Errorf("CLI token revoked")
	}

	// Update last_used_at asynchronously (best-effort)
	go func() {
		now := time.Now().UTC().Format(time.RFC3339)
		db.Exec("UPDATE cli_tokens SET last_used_at = ? WHERE token_hash = ?", now, tokenHash)
	}()

	return userID, email, name, nil
}
