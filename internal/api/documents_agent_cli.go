package api

import (
	"context"
	"database/sql"
)

// Preserve the Docs route's pages scopes while sharing exact credential
// validity with other human content writers. Always use the writer transaction.
func validateDocumentCLITokenTx(ctx context.Context, tx *sql.Tx, token, userID string, write bool) error {
	scope := "pages:read"
	if write {
		scope = "pages:write"
	}
	return validateContentCLIToken(ctx, tx, token, userID, scope)
}
