package api

import (
	"context"
	"fmt"
	"strings"
)

// Rotation grace on the delivery path (#1882).
//
// A rotation keeps the credential's previous encrypted value on its
// credential_rotations row for the grace window (credential_rotation.go). The
// sidecar is what needs it — a 401 to the NEW value is the moment the old one
// is the right answer — and the sidecar has exactly one plaintext supply line:
// the boot payload. So the previous value is attached HERE, to the delivered
// row, and rides beside the current value through every mirror struct down to
// the CredStore. It is never fetched later, never served by the metadata
// listing, never logged.
//
// Attached at the same chokepoint the parts and grantees are, for the same
// reason: a second derivation elsewhere is always the one that misses a filter.

// attachDeliveredCredentialGrace fills GraceEncryptedValue / GraceExpiresAt /
// GraceRotationID on every delivered row that has an ACTIVE, unexpired
// rotation with a value still on it. When a credential was rotated more than
// once inside one window, the MOST RECENT rotation's previous value is the
// one delivered — it is the value the sidecar that booted just before this
// one is running on.
func attachDeliveredCredentialGrace(ctx context.Context, db sqlQuerier, delivered []deliveredCredential) error {
	if len(delivered) == 0 {
		return nil
	}
	ids := make([]any, 0, len(delivered))
	seen := make(map[string]bool, len(delivered))
	for _, d := range delivered {
		if !seen[d.ID] {
			seen[d.ID] = true
			ids = append(ids, d.ID)
		}
	}

	// The ids come from agentDeliveredCredentialsSQL, already workspace-,
	// status- and lease-filtered, so no tenancy predicate of its own: a
	// rotation is reachable only through a credential this agent is delivered.
	// The three-way gate mirrors the fallback condition credential_rotation.go
	// promised — status ACTIVE, window open, value present — so a row the
	// hourly sweep has not reached yet, or one grace_seconds=0 wrote born-
	// expired, delivers nothing.
	query := `SELECT credential_id, id, old_value, expires_at
		FROM credential_rotations
		WHERE status = 'ACTIVE' AND expires_at > ? AND old_value != ''
		  AND credential_id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)
		ORDER BY credential_id, rotated_at DESC, id DESC`
	args := append([]any{leaseComparisonNow()}, ids...)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query credential rotations: %w", err)
	}
	defer rows.Close()

	type grace struct{ rotationID, encrypted, expiresAt string }
	byCred := make(map[string]grace)
	for rows.Next() {
		var credID string
		var g grace
		if err := rows.Scan(&credID, &g.rotationID, &g.encrypted, &g.expiresAt); err != nil {
			return fmt.Errorf("scan credential rotation: %w", err)
		}
		// First row per credential is the most recent (ORDER BY above).
		if _, ok := byCred[credID]; !ok {
			byCred[credID] = g
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range delivered {
		if g, ok := byCred[delivered[i].ID]; ok {
			delivered[i].GraceEncryptedValue = g.encrypted
			delivered[i].GraceExpiresAt = g.expiresAt
			delivered[i].GraceRotationID = g.rotationID
		}
	}
	return nil
}

// deliveredGraceToken turns a delivered row's rotation grace ciphertext into
// the token the sidecar will replay with, or "" when there is nothing usable.
//
// currentBaseURL is the upstream the CURRENT value resolved to, for a provider
// whose upstream lives in the credential (OPENAI_COMPAT). Its previous value
// is the same {baseURL,apiKey,headers} object, so it is split the same way —
// and kept only when it named the same upstream: a previous key for a
// different gateway is not a fallback for this one, and replaying it would
// send this gateway a secret that belongs to another.
//
// Every failure returns "" rather than an error. The current value's own
// failure policy (log and continue, or fail the delivery) is each loader's
// decision; a grace value is strictly optional and must never be the reason
// a credential is not delivered.
func deliveredGraceToken(d deliveredCredential, currentBaseURL string, decrypt func(string) (string, error)) string {
	if d.GraceEncryptedValue == "" {
		return ""
	}
	dec, err := decrypt(d.GraceEncryptedValue)
	if err != nil || isPendingSentinel(dec) {
		return ""
	}
	if !providerNeedsEndpointValue(d.Provider) {
		return dec
	}
	token, baseURL, _, err := providerEndpointFromValue(d.Provider, dec)
	if err != nil || baseURL != currentBaseURL {
		return ""
	}
	return token
}
