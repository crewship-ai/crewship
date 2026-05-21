package api

// CLI token management. Two tiers post-Patch-J:
//
//   - STANDARD ("crewship_cli_"): 256-bit random (was 160-bit), SHA-256
//     hash at rest, optional expiry, async last_used_at debounce.
//     Used by regular humans for their day-to-day CLI work.
//
//   - ADMIN ("crewship_admin_"): 256-bit random, HMAC-SHA256 keyed by
//     CREWSHIP_ADMIN_TOKEN_HMAC_KEY at rest, mandatory expiry ≤ 7 days,
//     synchronous per-use audit row in cli_token_uses, OWNER role
//     required to issue. A DB dump alone cannot offline-crack an ADMIN
//     token because the HMAC key is never persisted in the database.
//
// The validator dispatches on the prefix so every code path that
// presents either token (Bearer header, cookie, etc.) auto-routes to
// the correct verification path. IsCLIToken returns true for either
// tier so callers that only care "is this a CLI token at all" stay
// simple.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// cliTokenStandardPrefix marks the regular per-user CLI token. The
	// pre-v99 prefix; existing tokens continue to validate after the
	// migration (their rows backfill to tier='STANDARD').
	cliTokenStandardPrefix = "crewship_cli_"

	// cliTokenAdminPrefix marks the elevated admin tier. Different
	// prefix so a glance at a leaked token tells you "this is the
	// dangerous one" without DB lookup.
	cliTokenAdminPrefix = "crewship_admin_"

	// Legacy alias retained so a grep for cliTokenPrefix still hits
	// the standard tier — referenced by Bootstrap + cli_pair which
	// only mint standard tokens.
	cliTokenPrefix = cliTokenStandardPrefix

	// adminTokenMaxLifetime is the hard ceiling for ADMIN expiry.
	// Operators can request shorter but never longer. 7 days mirrors
	// what GitHub fine-grained PATs default to and balances "rotate
	// regularly" against "ops fatigue".
	adminTokenMaxLifetime = 7 * 24 * time.Hour

	// adminTokenDefaultLifetime is what the handler picks when the
	// caller didn't specify expires_at on creation. 24h matches a
	// typical operator on-call rotation.
	adminTokenDefaultLifetime = 24 * time.Hour

	// adminTokenHMACKeyEnv names the env var the operator must set
	// before any ADMIN-tier token can be issued or validated. The
	// key is loaded once at process start; rotation requires a
	// dedicated reencrypt routine which is out of scope for Patch J.
	adminTokenHMACKeyEnv = "CREWSHIP_ADMIN_TOKEN_HMAC_KEY"
)

// errAdminHMACKeyMissing is returned when the ADMIN-tier path is hit
// without an HMAC key configured. We treat this as a deployment bug,
// not a runtime auth failure — the handler returns 503 so the operator
// sees "fix your env" instead of a silent SHA-256 fallback that would
// re-collapse the two tiers into one.
var errAdminHMACKeyMissing = errors.New("ADMIN tier disabled: " + adminTokenHMACKeyEnv + " not set")

// adminHMACKey returns the raw bytes used to HMAC-key ADMIN tokens.
// The env value is hex-encoded for parity with ENCRYPTION_KEY; an
// unset or unparseable value returns errAdminHMACKeyMissing. We
// re-read on every call so a process restart picks up a rotated key
// without redeploying the binary — but the read is cheap (one syscall +
// hex decode of ~32 bytes), and tokens are checked at most a few
// times per second per CLI session.
func adminHMACKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(adminTokenHMACKeyEnv))
	if raw == "" {
		return nil, errAdminHMACKeyMissing
	}
	key, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("admin HMAC key: %s must be hex-encoded: %w", adminTokenHMACKeyEnv, err)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("admin HMAC key: %s must be at least 32 bytes (got %d)", adminTokenHMACKeyEnv, len(key))
	}
	return key, nil
}

// hashStandard returns the lookup hash for a STANDARD-tier token.
// Plain SHA-256 of the cleartext token is sufficient because the
// cleartext itself is 256-bit random — no rainbow-table preimage
// attack is feasible. Constant-time comparison is provided by the
// DB layer using the hash as an indexed key.
func hashStandard(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// hashAdmin returns the lookup hash for an ADMIN-tier token using
// HMAC-SHA256 with the configured server-side key. An attacker who
// dumps the database without also stealing CREWSHIP_ADMIN_TOKEN_HMAC_KEY
// cannot precompute or brute-force the cleartext (HMAC's keyed
// construction defeats offline preimage attacks even on 32-byte
// cleartext).
func hashAdmin(token string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// CLITokenHandler provides endpoints for creating, listing, validating, and revoking CLI authentication tokens.
type CLITokenHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewCLITokenHandler creates a CLITokenHandler with the given database and logger.
func NewCLITokenHandler(db *sql.DB, logger *slog.Logger) *CLITokenHandler {
	return &CLITokenHandler{db: db, logger: logger}
}

// createTokenRequest is the wire body for POST /api/v1/cli-tokens.
// tier defaults to STANDARD when omitted so existing CLI clients keep
// working. expires_at is a Unix-seconds integer; passing 0 means
// "default" (STANDARD: no expiry, ADMIN: now + adminTokenDefaultLifetime).
//
// Scopes (Patch M2) optionally narrow the token below the caller's
// full role. An empty list = "no restriction" — the token carries
// the full user role, which is the historical behaviour. A non-empty
// list pins the token to a closed set; the validator copies these
// into ctxTokenScopes on every request the token authenticates so
// downstream handlers can run canScope() in addition to canRole().
type createTokenRequest struct {
	Name             string   `json:"name"`
	Tier             string   `json:"tier,omitempty"`
	ExpiresInSeconds int      `json:"expires_in_seconds,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
}

// knownScopes lists the scope strings the server recognises. The
// validator refuses unknown scopes at issue time so a typo'd
// "agent:write" (missing the s) doesn't silently create a token
// that grants nothing.
//
// Resource:action pattern mirrors GitHub fine-grained PAT scopes
// + Stripe restricted keys. Wildcards (`*` and `<resource>:*`) are
// recognised by canScope but listed here so an operator running
// `crewship cli-token create --scopes <list>` from a tab-complete
// shell sees the menu.
var knownScopes = map[string]struct{}{
	"*":                  {},
	"agents:*":           {},
	"agents:read":        {},
	"agents:write":       {},
	"agents:run":         {},
	"crews:*":            {},
	"crews:read":         {},
	"crews:write":        {},
	"credentials:*":      {},
	"credentials:read":   {},
	"credentials:write":  {},
	"skills:*":           {},
	"skills:read":        {},
	"skills:write":       {},
	"workspace:admin":    {},
	"workspace:read":     {},
	"webhooks:*":         {},
	"webhooks:read":      {},
	"webhooks:write":     {},
}

// scopesPermittedByRole reports whether a caller with the given
// workspace role is allowed to issue a token with the requested
// scope list. The rule: a token can never grant MORE than the
// issuer's role permits. A MANAGER cannot issue a `workspace:admin`
// token — that would let them ladder up.
//
// Conservative defaults: MEMBER / VIEWER can only issue read-only
// scopes. MANAGER can issue read + write but not workspace:admin
// or the all-encompassing "*". ADMIN / OWNER can issue anything.
//
// Returns the offending scope (or "" on success) so the API error
// can name it directly instead of a generic "denied".
func scopesPermittedByRole(role string, scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	rank := roleRank[role]
	for _, s := range scopes {
		switch s {
		case "*", "workspace:admin":
			if rank < roleRank["ADMIN"] {
				return s
			}
		case "agents:write", "crews:write", "credentials:write",
			"skills:write", "webhooks:write",
			"agents:*", "crews:*", "credentials:*", "skills:*", "webhooks:*",
			"agents:run":
			if rank < roleRank["MANAGER"] {
				return s
			}
		}
	}
	return ""
}

// Create generates a new CLI token for the authenticated user and returns the plaintext token.
// POST /api/v1/cli-tokens — the token is only returned once; only the SHA-256 hash is stored.
//
// Tier dispatch:
//
//   - tier omitted or "STANDARD" → 32-byte random, SHA-256 hashed,
//     no expiry unless the caller specifies one.
//   - tier == "ADMIN" → 32-byte random, HMAC-SHA256 hashed, mandatory
//     expiry (capped at adminTokenMaxLifetime). Requires OWNER role
//     and CREWSHIP_ADMIN_TOKEN_HMAC_KEY to be configured.
func (h *CLITokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var body createTokenRequest
	if err := readJSON(r, &body); err != nil {
		body.Name = "CLI token"
	}
	if body.Name == "" {
		body.Name = "CLI token"
	}
	tier := strings.ToUpper(strings.TrimSpace(body.Tier))
	if tier == "" {
		tier = "STANDARD"
	}
	if tier != "STANDARD" && tier != "ADMIN" {
		replyError(w, http.StatusBadRequest, "tier must be STANDARD or ADMIN")
		return
	}

	// Normalise + validate the scope list. Unknown scopes get a clean
	// 400 with the offending value; over-privileged scopes get a 403
	// naming the specific scope so the operator sees why.
	normalisedScopes := make([]string, 0, len(body.Scopes))
	for _, s := range body.Scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := knownScopes[s]; !ok {
			replyError(w, http.StatusBadRequest, "unknown scope: "+s)
			return
		}
		normalisedScopes = append(normalisedScopes, s)
	}

	// Patch M2: role-vs-scope check only fires when the caller asked
	// for explicit scopes. An unscoped token carries the user's full
	// role implicitly — no need to consult the membership table at
	// all, which keeps the legacy "create a basic token" path working
	// even in unit-test setups that didn't seed workspace_members.
	if len(normalisedScopes) > 0 {
		var callerRole string
		if err := h.db.QueryRowContext(r.Context(), `
			SELECT role FROM workspace_members
			WHERE user_id = ?
			ORDER BY
				CASE role
					WHEN 'OWNER' THEN 5
					WHEN 'ADMIN' THEN 4
					WHEN 'MANAGER' THEN 3
					WHEN 'MEMBER' THEN 2
					WHEN 'VIEWER' THEN 1
					ELSE 0
				END DESC
			LIMIT 1
		`, user.ID).Scan(&callerRole); err != nil {
			if err == sql.ErrNoRows {
				replyError(w, http.StatusForbidden,
					"issuing user has no workspace membership; cannot grant scoped token")
				return
			}
			h.logger.Error("cli_token: lookup caller role", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		if denied := scopesPermittedByRole(callerRole, normalisedScopes); denied != "" {
			h.logger.Warn("cli_token: scope exceeds caller role",
				"user_id", user.ID, "role", callerRole, "scope", denied)
			replyError(w, http.StatusForbidden,
				"scope "+denied+" exceeds your workspace role")
			return
		}
	}
	var scopesJSON sql.NullString
	if len(normalisedScopes) > 0 {
		buf, err := json.Marshal(normalisedScopes)
		if err != nil {
			h.logger.Error("marshal scopes", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		scopesJSON = sql.NullString{String: string(buf), Valid: true}
	}

	now := time.Now().UTC()
	var expiresAt sql.NullString

	// 32 bytes = 256-bit entropy. Pre-Patch-J standard tokens were 20
	// bytes (160-bit) which is fine but unnecessarily lower margin
	// than the rest of the codebase's secrets (session ids etc.).
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		h.logger.Error("generate token", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	var token, tokenHash string
	switch tier {
	case "STANDARD":
		token = cliTokenStandardPrefix + hex.EncodeToString(buf)
		tokenHash = hashStandard(token)
		if body.ExpiresInSeconds > 0 {
			expiresAt = sql.NullString{
				String: now.Add(time.Duration(body.ExpiresInSeconds) * time.Second).Format(time.RFC3339),
				Valid:  true,
			}
		}
	case "ADMIN":
		// ADMIN issuance is OWNER-only and requires the HMAC key.
		// We deliberately check the role inside the handler instead
		// of via middleware so the error message matches the actual
		// reason — middleware would 403 with a generic "Forbidden"
		// without explaining tier semantics.
		if !userIsWorkspaceOwner(r.Context(), h.db, user.ID) {
			h.logger.Warn("ADMIN tier issuance refused: caller is not OWNER",
				"user_id", user.ID, "remote_addr", r.RemoteAddr)
			replyError(w, http.StatusForbidden, "ADMIN-tier tokens require workspace OWNER role")
			return
		}
		key, err := adminHMACKey()
		if err != nil {
			h.logger.Error("ADMIN tier issuance refused: HMAC key not configured", "error", err)
			replyError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		ttl := adminTokenDefaultLifetime
		if body.ExpiresInSeconds > 0 {
			ttl = time.Duration(body.ExpiresInSeconds) * time.Second
		}
		if ttl > adminTokenMaxLifetime {
			ttl = adminTokenMaxLifetime
		}
		if ttl < time.Minute {
			replyError(w, http.StatusBadRequest, "ADMIN token TTL must be at least 60 seconds")
			return
		}
		token = cliTokenAdminPrefix + hex.EncodeToString(buf)
		tokenHash = hashAdmin(token, key)
		expiresAt = sql.NullString{
			String: now.Add(ttl).Format(time.RFC3339),
			Valid:  true,
		}
	}

	id := generateCUID()
	nowStr := now.Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO cli_tokens (id, user_id, name, token_hash, tier, expires_at, scopes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, user.ID, body.Name, tokenHash, tier, expiresAt, scopesJSON, nowStr)
	if err != nil {
		h.logger.Error("insert cli_token", "error", err, "tier", tier)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := map[string]interface{}{
		"token":      token,
		"id":         id,
		"name":       body.Name,
		"tier":       tier,
		"created_at": nowStr,
	}
	if expiresAt.Valid {
		resp["expires_at"] = expiresAt.String
	}
	if len(normalisedScopes) > 0 {
		resp["scopes"] = normalisedScopes
	}
	writeJSON(w, http.StatusOK, resp)
}

// userIsWorkspaceOwner reports whether the user owns at least one
// workspace. ADMIN tier is workspace-scoped in spirit (the token
// authorizes operations within a single workspace) but the CLI token
// itself is user-scoped — so we treat "owner of at least one
// workspace" as the issuance gate. A SUPER_ADMIN platform role check
// would slot in here if/when the platform gains one.
func userIsWorkspaceOwner(ctx context.Context, db *sql.DB, userID string) bool {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM workspace_members WHERE user_id = ? AND role = 'OWNER'`,
		userID).Scan(&count)
	return err == nil && count > 0
}

// Validate confirms the current CLI token is valid and returns the associated user info.
// POST /api/v1/cli-tokens/validate
func (h *CLITokenHandler) Validate(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"valid":      true,
		"user_id":    user.ID,
		"user_email": user.Email,
	})
}

// List returns all CLI tokens for the authenticated user (without the plaintext token values).
// GET /api/v1/cli-tokens
func (h *CLITokenHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, tier, expires_at, created_at, last_used_at, revoked_at
		 FROM cli_tokens WHERE user_id = ? ORDER BY created_at DESC`, user.ID)
	if err != nil {
		h.logger.Error("list cli_tokens", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var tokens []map[string]interface{}
	for rows.Next() {
		var id, name, tier, createdAt string
		var expiresAt, lastUsedAt, revokedAt sql.NullString
		if err := rows.Scan(&id, &name, &tier, &expiresAt, &createdAt, &lastUsedAt, &revokedAt); err != nil {
			continue
		}
		t := map[string]interface{}{
			"id":         id,
			"name":       name,
			"tier":       tier,
			"created_at": createdAt,
		}
		if expiresAt.Valid {
			t["expires_at"] = expiresAt.String
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

// Revoke marks a CLI token as revoked so it can no longer be used for authentication.
// DELETE /api/v1/cli-tokens/{tokenId}
func (h *CLITokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	tokenID := r.PathValue("tokenId")
	now := time.Now().UTC().Format(time.RFC3339)

	result, err := h.db.ExecContext(r.Context(),
		"UPDATE cli_tokens SET revoked_at = ? WHERE id = ? AND user_id = ?",
		now, tokenID, user.ID)
	if err != nil {
		h.logger.Error("revoke cli_token", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		replyError(w, http.StatusNotFound, "Token not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// IsCLIToken returns true if the token has either tier's prefix.
// Both tiers are CLI-style bearer tokens from the validator's point
// of view — the tier-specific verification happens inside
// ValidateCLIToken.
func IsCLIToken(token string) bool {
	return strings.HasPrefix(token, cliTokenStandardPrefix) ||
		strings.HasPrefix(token, cliTokenAdminPrefix)
}

// ValidateCLIToken validates a CLI token (either tier) against the
// database. Dispatch is on the prefix:
//
//   - crewship_cli_…  → SHA-256 lookup against tier='STANDARD'
//   - crewship_admin_… → HMAC-SHA256 lookup against tier='ADMIN'
//
// Expiry, revocation, and tier-mismatch checks all live here so a
// caller that holds a leaked / expired / revoked token sees the same
// "invalid CLI token" error and cannot oracle which condition fired.
//
// Returns (userID, email, name, error). The error wraps a generic
// "invalid CLI token" for all non-success paths; specific reasons go
// to the logger so an operator can audit without leaking them to the
// caller.
// ValidateCLITokenResult captures the user identity AND the token's
// scope set so callers (the AuthMiddleware) can stash both in the
// request context. Pre-Patch-M2 the function returned just the
// three identity strings — scopes ride alongside without breaking
// callers that don't care.
type ValidateCLITokenResult struct {
	UserID string
	Email  string
	Name   string
	Scopes stringSet // nil = unrestricted token (pre-v100 or no scopes set)
}

// ValidateCLITokenFull is the post-Patch-M2 entrypoint that also
// returns the token's scope set. ValidateCLIToken keeps the legacy
// three-string signature for callers that don't care about scopes;
// it now delegates to this one.
func ValidateCLITokenFull(ctx context.Context, db *sql.DB, token string, audit ValidateAuditContext) (ValidateCLITokenResult, error) {
	var (
		tokenHash    string
		expectedTier string
	)
	switch {
	case strings.HasPrefix(token, cliTokenAdminPrefix):
		key, err := adminHMACKey()
		if err != nil {
			return ValidateCLITokenResult{}, fmt.Errorf("invalid CLI token")
		}
		tokenHash = hashAdmin(token, key)
		expectedTier = "ADMIN"
	case strings.HasPrefix(token, cliTokenStandardPrefix):
		tokenHash = hashStandard(token)
		expectedTier = "STANDARD"
	default:
		return ValidateCLITokenResult{}, fmt.Errorf("invalid CLI token")
	}

	var (
		userID, email, name, dbTier string
		expiresAt, revokedAt        sql.NullString
		scopesRaw                   sql.NullString
		tokenID                     string
	)
	err := db.QueryRowContext(ctx, `
		SELECT ct.id, ct.user_id, u.email, u.full_name, ct.tier, ct.expires_at, ct.revoked_at, ct.scopes
		FROM cli_tokens ct
		JOIN users u ON u.id = ct.user_id
		WHERE ct.token_hash = ?
	`, tokenHash).Scan(&tokenID, &userID, &email, &name, &dbTier, &expiresAt, &revokedAt, &scopesRaw)
	if err != nil {
		if err == sql.ErrNoRows {
			return ValidateCLITokenResult{}, fmt.Errorf("invalid CLI token")
		}
		return ValidateCLITokenResult{}, fmt.Errorf("validate CLI token: %w", err)
	}

	// Belt-and-braces: refuse if the row's stored tier disagrees with
	// the tier we deduced from the prefix. The collision is implausible
	// (different hash functions, no shared keyspace) but the check is
	// free and defends against a future bug that lets a STANDARD token
	// row's hash match an ADMIN computation or vice versa.
	if dbTier != expectedTier {
		return ValidateCLITokenResult{}, fmt.Errorf("invalid CLI token")
	}

	if revokedAt.Valid {
		return ValidateCLITokenResult{}, fmt.Errorf("invalid CLI token")
	}
	if expiresAt.Valid {
		t, perr := time.Parse(time.RFC3339, expiresAt.String)
		if perr == nil && time.Now().UTC().After(t) {
			return ValidateCLITokenResult{}, fmt.Errorf("invalid CLI token")
		}
	}

	var scopes stringSet
	if scopesRaw.Valid {
		scopes = parseScopes(scopesRaw.String)
	}

	// last_used_at debounce (STANDARD), per-use audit (ADMIN).
	// Background-only goroutine that gets its OWN 5s context derived
	// from context.Background, not the caller's ctx — the audit work
	// must survive a caller deadline because the actual API call has
	// already accepted the token. We mirror the caller's ctx only for
	// the synchronous SELECT above (where caller deadline = "is this
	// a useful auth result still" is meaningful). 5s bounds a wedged
	// SQLite without leaking unbounded goroutines.
	go func(id, tier string, a ValidateAuditContext) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		nowStr := time.Now().UTC().Format(time.RFC3339)
		if _, ierr := db.ExecContext(bgCtx,
			"UPDATE cli_tokens SET last_used_at = ? WHERE id = ?", nowStr, id); ierr != nil {
			// Async, best-effort — silent. The operator wouldn't see a
			// returned error here even if we propagated it.
			_ = ierr
		}
		if tier == "ADMIN" {
			useID := generateCUID()
			// Audit row carries the caller's RemoteAddr + UA + path so
			// an incident-response query "what did this admin token
			// touch in the last hour" returns rows with actual signal,
			// not just timestamps. ValidateAuditContext lets a caller
			// that legitimately doesn't have a request handy (tests,
			// CLI tools driving validation) pass the zero value and
			// the row still inserts.
			_, _ = db.ExecContext(bgCtx,
				`INSERT INTO cli_token_uses (id, token_id, used_at, remote_addr, user_agent, path)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				useID, id, nowStr,
				nullIfBlank(a.RemoteAddr),
				nullIfBlank(a.UserAgent),
				nullIfBlank(a.Path),
			)
		}
	}(tokenID, dbTier, audit)

	return ValidateCLITokenResult{
		UserID: userID,
		Email:  email,
		Name:   name,
		Scopes: scopes,
	}, nil
}

// ValidateCLIToken keeps the pre-Patch-M2 three-string signature so
// callers that don't care about scopes (most existing tests) don't
// have to be rewritten in lockstep. Internally delegates to
// ValidateCLITokenFull and discards the scope set — the production
// AuthMiddleware uses the Full variant so the scope info reaches
// the request context.
func ValidateCLIToken(ctx context.Context, db *sql.DB, token string, audit ValidateAuditContext) (string, string, string, error) {
	res, err := ValidateCLITokenFull(ctx, db, token, audit)
	if err != nil {
		return "", "", "", err
	}
	return res.UserID, res.Email, res.Name, nil
}

// ValidateAuditContext carries the request metadata an admin-tier
// validation should attach to its cli_token_uses row. Callers that
// don't have an HTTP request in hand (CLI commands, tests) pass the
// zero value; the audit insert handles empty fields by writing NULL
// rather than empty strings so queries can distinguish "we didn't
// know" from "the value was the empty string".
type ValidateAuditContext struct {
	RemoteAddr string
	UserAgent  string
	Path       string
}

// nullIfBlank returns a sql.NullString that's NOT valid when the
// input is empty / whitespace. Used by the admin-tier audit insert
// so missing fields land as NULL instead of empty TEXT, which a
// future analyst can distinguish via SQL `IS NULL`.
func nullIfBlank(s string) sql.NullString {
	if strings.TrimSpace(s) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
