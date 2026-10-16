package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Call through the operation's authority reader after its writer fence. Exact
// credential validity is separate from live membership and object ACL checks.
// requiredScope is the existing route scope; scopeSelf means validity only.
func validateContentHumanCredential(r *http.Request, reader pageAuthQueryer, requiredScope string) error {
	if strings.TrimSpace(requiredScope) == "" {
		return errors.New("explicit credential scope required")
	}
	user := UserFromContext(r.Context())
	if user == nil {
		return errInvalidCLIToken
	}
	switch AuthKindFromContext(r.Context()) {
	case AuthKindCLIToken:
		return validateContentCLIToken(r.Context(), reader, extractToken(r), user.ID, requiredScope)
	case AuthKindSession:
		var expiry string
		var revoked sql.NullString
		if user.SessionID == "" || reader.QueryRowContext(r.Context(), `SELECT expires_at,revoked_at FROM user_sessions WHERE id=? AND user_id=?`, user.SessionID, user.ID).Scan(&expiry, &revoked) != nil || revoked.Valid {
			return errors.New("current session required")
		}
		limit, err := time.Parse(time.RFC3339Nano, expiry)
		if err != nil || !time.Now().Before(limit) {
			return errors.New("current session required")
		}
		return nil
	default:
		return errors.New("known human credential required")
	}
}

func validateContentCLIToken(ctx context.Context, reader pageAuthQueryer, token, userID, requiredScope string) error {
	if strings.TrimSpace(requiredScope) == "" {
		return errors.New("explicit credential scope required")
	}
	var digest, expected string
	switch {
	case strings.HasPrefix(token, cliTokenAdminPrefix):
		key, err := adminHMACKey()
		if err != nil {
			return errInvalidCLIToken
		}
		digest = hashAdmin(token, key)
		expected = "ADMIN"
	case strings.HasPrefix(token, cliTokenStandardPrefix):
		digest = hashStandard(token)
		expected = "STANDARD"
	default:
		return errInvalidCLIToken
	}
	var tier string
	var expiry, revoked, scopes sql.NullString
	err := reader.QueryRowContext(ctx, `SELECT c.tier,c.expires_at,c.revoked_at,c.scopes FROM cli_tokens c JOIN users u ON u.id=c.user_id WHERE c.token_hash=? AND c.user_id=? AND u.suspended_at IS NULL`, digest, userID).Scan(&tier, &expiry, &revoked, &scopes)
	if err != nil || tier != expected || revoked.Valid {
		return errInvalidCLIToken
	}
	if expiry.Valid {
		limit, err := time.Parse(time.RFC3339Nano, expiry.String)
		if err != nil || !time.Now().Before(limit) {
			return errInvalidCLIToken
		}
	}
	if requiredScope != scopeSelf && scopes.Valid {
		set := parseScopes(scopes.String)
		if set != nil && !canScope(context.WithValue(ctx, ctxTokenScopes, set), requiredScope) {
			return errors.New("current CLI scope required: " + requiredScope)
		}
	}
	return nil
}
