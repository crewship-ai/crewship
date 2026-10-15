package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Validate inside the document writer transaction, so revocation cannot race a
// durable mutation and the transaction never waits for its own DB connection.
func validateDocumentCLITokenTx(ctx context.Context, tx *sql.Tx, token, userID string, write bool) error {
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
	err := tx.QueryRowContext(ctx, `SELECT c.tier,c.expires_at,c.revoked_at,c.scopes FROM cli_tokens c JOIN users u ON u.id=c.user_id WHERE c.token_hash=? AND c.user_id=? AND u.suspended_at IS NULL`, digest, userID).Scan(&tier, &expiry, &revoked, &scopes)
	if err != nil || tier != expected || revoked.Valid {
		return errInvalidCLIToken
	}
	if expiry.Valid {
		limit, err := time.Parse(time.RFC3339Nano, expiry.String)
		if err != nil || !time.Now().Before(limit) {
			return errInvalidCLIToken
		}
	}
	if scopes.Valid {
		set := parseScopes(scopes.String)
		if set != nil {
			scope := "pages:read"
			if write {
				scope = "pages:write"
			}
			if !canScope(context.WithValue(ctx, ctxTokenScopes, set), scope) {
				return errors.New("document CLI scope required: " + scope)
			}
		}
	}
	return nil
}
