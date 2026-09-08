package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// storeProviderLoginParts writes a split login's parts into credential_fields
// (docs/prd/provider-logins.md §10.2), seals the row when it carries a
// refresh token, and seeds the refresh state for a login the server will
// renew. Runs inside the create transaction.
//
// Secret parts (refresh_token, id_token) are encrypted with the same opener
// the value used; identifiers (account_id, plan, expires_at, mode) are
// cleartext like every other non-secret part. The refresh token makes the
// whole row SEALED: nothing on it — not even the access token — can be
// revealed, and the reveal surface refuses SEALED for every role. A login
// without one (a setup-token, a key) keeps STANDARD, exactly as its
// AI_CLI_TOKEN / API_KEY twin does today.
func storeProviderLoginParts(ctx context.Context, tx *sql.Tx, credID string, l providerlogin.Login, now string) error {
	type part struct {
		key    string
		value  string
		secret bool
	}
	parts := []part{
		{providerlogin.PartRefreshToken, l.RefreshToken, true},
		{providerlogin.PartIDToken, l.IDToken, true},
		{providerlogin.PartAccountID, l.AccountID, false},
		{providerlogin.PartPlan, l.Plan, false},
		{providerlogin.PartExpiresAt, "", false},
		{providerlogin.PartMode, l.Mode, false},
		{providerlogin.PartScope, l.Scope, false},
	}
	if !l.ExpiresAt.IsZero() {
		parts[4].value = l.ExpiresAt.UTC().Format(time.RFC3339)
	}
	ordinal := 0
	for _, p := range parts {
		if p.value == "" {
			continue
		}
		var cleartext, ciphertext any
		if p.secret {
			enc, err := encryption.Encrypt(p.value)
			if err != nil {
				return fmt.Errorf("encrypt provider login part %s: %w", p.key, err)
			}
			ciphertext = enc
		} else {
			cleartext = p.value
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO credential_fields (credential_id, key, value, encrypted_value, is_secret, ordinal)
			VALUES (?, ?, ?, ?, ?, ?)`,
			credID, p.key, cleartext, ciphertext, boolToInt(p.secret), ordinal); err != nil {
			return fmt.Errorf("store provider login part %s: %w", p.key, err)
		}
		ordinal++
	}
	if !l.RefreshSupported() {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE credentials SET sensitivity = ? WHERE id = ?`, SensitivitySealed, credID); err != nil {
		return fmt.Errorf("seal provider login: %w", err)
	}
	nextAt := ""
	if !l.ExpiresAt.IsZero() {
		nextAt = l.ExpiresAt.Add(-providerlogin.RefreshLeadFor(l.Provider)).UTC().Format(time.RFC3339)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO provider_login_refresh (credential_id, status, next_at, updated_at) VALUES (?, ?, NULLIF(?, ''), ?)
		ON CONFLICT(credential_id) DO UPDATE SET status = excluded.status, next_at = excluded.next_at,
			error = NULL, failures = 0, in_progress_until = NULL, updated_at = excluded.updated_at`,
		credID, providerlogin.StatusOK, nextAt, now)
	if err != nil {
		return fmt.Errorf("seed provider login refresh: %w", err)
	}
	return nil
}
