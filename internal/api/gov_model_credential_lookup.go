package api

import (
	"context"
	"database/sql"
	"fmt"
)

// govModelCredentialLookup implements governance.CredentialLookup (the seam
// governance.ResolveGovModel consumes) against the vault. It resolves a
// governance-model credential by id, scoped to a workspace, and returns its type
// plus the usable secret value.
//
// Revoke-safety (§4.4, #1001): the query requires the credential be ACTIVE and
// NOT soft-deleted (deleted_at IS NULL). A revoke is a soft delete
// (credentials.deleted_at set) / status flip, so a revoked credential no longer
// matches and this returns an error — which ResolveGovModel treats as the
// trigger to degrade to the default OLLAMA judge, never a hard failure. This is
// the *runtime* half of revoke-safety; the FK ON DELETE SET NULL only covers a
// hard-delete purge (see the v142 migration comment).
type govModelCredentialLookup struct {
	db *sql.DB
}

// newGovModelCredentialLookup builds the lookup over the given DB handle.
func newGovModelCredentialLookup(db *sql.DB) *govModelCredentialLookup {
	return &govModelCredentialLookup{db: db}
}

// LookupCredential returns the credential's type and its usable value:
//   - ENDPOINT_URL → the base URL (a destination, not a secret; the embedded
//     auth token/headers of a structured {baseURL,apiKey,headers} value are NOT
//     surfaced through this single-value seam — an openai_compat endpoint that
//     needs a key falls back to the provider builder's env key. Enhancing the
//     seam to carry both is a follow-up.)
//   - API_KEY → the decrypted key.
//
// A missing/revoked/soft-deleted credential, a decrypt failure, an
// unusable type, or a PENDING sentinel value all return a non-nil error so the
// resolver degrades rather than building a broken provider.
func (l *govModelCredentialLookup) LookupCredential(ctx context.Context, workspaceID, credentialID string) (string, string, error) {
	if l == nil || l.db == nil {
		return "", "", fmt.Errorf("gov-model credential lookup: no db handle")
	}
	if workspaceID == "" || credentialID == "" {
		return "", "", fmt.Errorf("gov-model credential lookup: empty workspace or credential id")
	}

	var encrypted, credType string
	err := l.db.QueryRowContext(ctx, `
		SELECT encrypted_value, type FROM credentials
		WHERE id = ? AND workspace_id = ? AND status = 'ACTIVE' AND deleted_at IS NULL`,
		credentialID, workspaceID).Scan(&encrypted, &credType)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("gov-model credential %q not found, inactive, or revoked in workspace %q", credentialID, workspaceID)
	}
	if err != nil {
		return "", "", fmt.Errorf("gov-model credential lookup: %w", err)
	}

	dec, err := decryptCredential(encrypted)
	if err != nil {
		return "", "", fmt.Errorf("gov-model credential %q decrypt: %w", credentialID, err)
	}
	if dec == "" || isPendingSentinel(dec) {
		return "", "", fmt.Errorf("gov-model credential %q has no usable value yet (pending)", credentialID)
	}

	switch credType {
	case CredTypeEndpointURL:
		baseURL, _, _, perr := parseEndpointValue(dec)
		if perr != nil || baseURL == "" {
			return "", "", fmt.Errorf("gov-model endpoint credential %q is unusable: %v", credentialID, perr)
		}
		return CredTypeEndpointURL, baseURL, nil
	case CredTypeAPIKey:
		return CredTypeAPIKey, dec, nil
	default:
		// Governance only routes ENDPOINT_URL / API_KEY; anything else is a
		// mis-selection the resolver should degrade on rather than misuse.
		return credType, dec, nil
	}
}
