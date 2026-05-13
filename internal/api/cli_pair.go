package api

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CLI pairing — a device-code flow (RFC 8628 in spirit) for handing a
// Crewship session to a user's locally-installed CLI: Claude Code,
// Gemini CLI, Codex, OpenCode, Cursor, Factory Droid, or anything we
// add later. The flow:
//
//  1. UI calls /pair/start (authed). Server creates a row in
//     cli_pairings with a fresh 8-char base32 code (human-typeable),
//     status='pending', 10-min TTL. Returns the code to the UI.
//
//  2. UI displays the code in a snippet `crewship login --pair --code=…`
//     and polls /pair/poll every ~2 seconds, waiting for the row's
//     status to flip to 'consumed'.
//
//  3. User runs the snippet on the machine where their CLI is
//     installed. The CLI calls /pair/redeem with the code; the
//     endpoint is *unauthenticated* by design — the code itself is
//     the credential, single-use, time-limited. Redeem mints a fresh
//     cli_tokens row and hands back the raw token.
//
//  4. CLI saves the token in ~/.crewship/cli-config.yaml. UI poll sees
//     status='consumed' and advances onboarding.
//
// adapter_hint is *telemetry only*. The backend MUST NOT route on it.
// When adapter #7 ships, the only required change is a new entry in
// lib/cli-adapters.ts on the frontend — zero backend touch.

// pairingCodeTTL is how long an issued code stays valid. 10 minutes
// matches the GitHub device-code default; long enough for an operator
// to switch terminals and run the snippet, short enough that a
// half-completed flow doesn't leave actionable codes lying around.
const pairingCodeTTL = 10 * time.Minute

// pairingCodeAlphabet uses Crockford base32 minus visually ambiguous
// characters (no 0/O, 1/I/L). Operators type these by hand.
const pairingCodeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// CliPairHandler owns the pair/start, pair/poll, and pair/redeem
// endpoints. Construction is intentionally trivial — every route
// reaches the database directly, no service layer, because the flow
// is small enough to read top-to-bottom in this one file.
type CliPairHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewCliPairHandler constructs a CliPairHandler.
func NewCliPairHandler(db *sql.DB, logger *slog.Logger) *CliPairHandler {
	return &CliPairHandler{db: db, logger: logger}
}

type pairStartRequest struct {
	AdapterHint string `json:"adapter_hint"`
}

type pairStartResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

// Start creates a new pairing code for the authenticated user.
// POST /api/v1/cli/pair/start
func (h *CliPairHandler) Start(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	var req pairStartRequest
	// Body is optional — UI may not send anything. ReadJSON tolerates
	// empty body via the existing helper conventions.
	_ = readJSON(r, &req)

	// adapter_hint is stored verbatim if it looks like one of the
	// known adapter keys (letters + underscore + digits), capped at
	// 32 chars to keep the column small. Anything else is logged and
	// stripped — telemetry isn't worth a log-injection vector.
	hint := sanitizeAdapterHint(req.AdapterHint)

	code, err := generatePairingCode(8)
	if err != nil {
		h.logger.Error("cli pair: generate code", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	now := time.Now().UTC()
	expires := now.Add(pairingCodeTTL)
	id := generateCUID()

	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO cli_pairings (id, user_id, code, status, adapter_hint, created_at, expires_at)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)`,
		id, user.ID, code, nullableHint(hint), now.Format(time.RFC3339), expires.Format(time.RFC3339)); err != nil {
		h.logger.Error("cli pair: insert", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, pairStartResponse{
		Code:      code,
		ExpiresAt: expires.Format(time.RFC3339),
	})
}

type pairPollResponse struct {
	Status      string `json:"status"`
	AdapterHint string `json:"adapter_hint,omitempty"`
	ExpiresAt   string `json:"expires_at"`
}

// Poll reports the status of an outstanding pairing. The caller must
// own the row (user_id match) — otherwise the row is reported as
// 'expired' to avoid leaking the existence of another user's code.
// GET /api/v1/cli/pair/poll?code=XXXX-XXXX
func (h *CliPairHandler) Poll(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}

	code := normalizePairingCode(r.URL.Query().Get("code"))
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code is required"})
		return
	}

	var status, expiresStr string
	var hint sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT status, COALESCE(adapter_hint, ''), expires_at
		FROM cli_pairings WHERE code = ? AND user_id = ?`, code, user.ID).Scan(&status, &hint, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		// Don't distinguish "wrong user" from "no such code" — both
		// look like 'expired' to the caller. Avoids using poll as a
		// code-enumeration oracle.
		writeJSON(w, http.StatusOK, pairPollResponse{Status: "expired"})
		return
	}
	if err != nil {
		h.logger.Error("cli pair: poll lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if status == "pending" {
		if expires, err := time.Parse(time.RFC3339, expiresStr); err == nil && time.Now().UTC().After(expires) {
			status = "expired"
			// Best-effort flip so the next poll doesn't keep
			// re-checking a dead row.
			_, _ = h.db.ExecContext(r.Context(),
				"UPDATE cli_pairings SET status='expired' WHERE code = ?", code)
		}
	}

	resp := pairPollResponse{Status: status, ExpiresAt: expiresStr}
	if hint.Valid {
		resp.AdapterHint = hint.String
	}
	writeJSON(w, http.StatusOK, resp)
}

type pairRedeemRequest struct {
	Code        string `json:"code"`
	AdapterHint string `json:"adapter_hint"`
}

type pairRedeemResponse struct {
	CliToken string `json:"cli_token"`
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
}

// Redeem exchanges a pairing code for a fresh cli_tokens row. The
// endpoint is unauthenticated: the code IS the credential. Single-use
// (CAS to consumed inside a transaction), 10-minute TTL, and the
// authRateLimitedMux at the router level caps callers at 10
// req/min/IP so a bored attacker can't iterate the 30^8 keyspace.
//
// POST /api/v1/cli/pair/redeem
func (h *CliPairHandler) Redeem(w http.ResponseWriter, r *http.Request) {
	var req pairRedeemRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	code := normalizePairingCode(req.Code)
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code is required"})
		return
	}
	hint := sanitizeAdapterHint(req.AdapterHint)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("cli pair redeem: begin tx", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Lock + validate the pairing row in the transaction. SQLite
	// serializes writers, so the SELECT-then-UPDATE here is safe
	// against a concurrent second redeem.
	var userID, status, expiresStr string
	err = tx.QueryRowContext(r.Context(), `
		SELECT user_id, status, expires_at FROM cli_pairings WHERE code = ?`, code).Scan(&userID, &status, &expiresStr)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired code"})
		return
	}
	if err != nil {
		h.logger.Error("cli pair redeem: lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if status != "pending" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired code"})
		return
	}
	expires, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil || time.Now().UTC().After(expires) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid or expired code"})
		return
	}

	// Mint the CLI token — same shape as the bootstrap path.
	tokenBytes := make([]byte, 20)
	if _, err := rand.Read(tokenBytes); err != nil {
		h.logger.Error("cli pair redeem: rand", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	cliToken := cliTokenPrefix + hex.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(cliToken))
	tokenHashHex := hex.EncodeToString(tokenHash[:])
	tokenID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	tokenName := "pair"
	if hint != "" {
		tokenName = "pair-" + strings.ToLower(hint)
	}

	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		tokenID, userID, tokenName, tokenHashHex, now); err != nil {
		h.logger.Error("cli pair redeem: insert cli_token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE cli_pairings SET status='consumed', consumed_at=?, adapter_hint=COALESCE(?, adapter_hint)
		WHERE code = ? AND status='pending'`,
		now, nullableHint(hint), code); err != nil {
		h.logger.Error("cli pair redeem: mark consumed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var email string
	if err := tx.QueryRowContext(r.Context(), "SELECT email FROM users WHERE id = ?", userID).Scan(&email); err != nil {
		h.logger.Error("cli pair redeem: lookup user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("cli pair redeem: commit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.logger.Info("cli pair redeemed", "user_id", userID, "adapter_hint", hint)
	writeJSON(w, http.StatusOK, pairRedeemResponse{
		CliToken: cliToken,
		UserID:   userID,
		Email:    email,
	})
}

// generatePairingCode returns an XXXX-XXXX style human-typeable code
// of (length) total alphabet chars (i.e. length=8 yields 8 chars
// formatted as 4-4). Uses Crockford-ish base32 minus 0/O/1/I/L for
// fewer typo reports.
func generatePairingCode(n int) (string, error) {
	buf := make([]byte, n)
	rb := make([]byte, n)
	if _, err := rand.Read(rb); err != nil {
		return "", err
	}
	for i, b := range rb {
		buf[i] = pairingCodeAlphabet[int(b)%len(pairingCodeAlphabet)]
	}
	// 4-4 split for legibility. Higher lengths could grow the
	// pattern (4-4-4) but 8 is plenty: 30^8 ≈ 6.5e11 keyspace, with
	// a 10-min TTL and rate-limited /redeem.
	if n == 8 {
		return string(buf[:4]) + "-" + string(buf[4:]), nil
	}
	return string(buf), nil
}

// normalizePairingCode uppercases the input and strips dashes/spaces
// so the user can type "k3f9 x2nm" or "k3f9-x2nm" or "K3F9X2NM" and
// hit the same row.
func normalizePairingCode(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 8 {
		return ""
	}
	// Re-insert the canonical dash so the DB lookup matches the
	// stored format.
	return s[:4] + "-" + s[4:]
}

// sanitizeAdapterHint allows only [A-Z_0-9] up to 32 chars. Anything
// else is dropped to avoid log-injection or sketchy values landing
// in the column. *Strictly* telemetry — read by nobody but a future
// usage dashboard.
func sanitizeAdapterHint(raw string) string {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if len(s) > 32 {
		s = s[:32]
	}
	for _, r := range s {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return ""
		}
	}
	return s
}

// nullableHint turns "" into a SQL NULL, anything else into the
// trimmed string. Avoids storing empty strings that would mis-read
// later as "set but blank."
func nullableHint(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
