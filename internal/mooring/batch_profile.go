package mooring

import (
	"context"
	"database/sql"
	"errors"
)

const LegacyCollaborationProfile = "mooring-causal-v1"

// Legacy payload adapters must reject newer document encodings independently
// of the feature flag that created them. No bytes are decoded or rewritten.
func requireLegacyCollaborationProfile(ctx context.Context, tx *sql.Tx, key SessionKey) error {
	var profile string
	err := tx.QueryRowContext(ctx, `SELECT format_profile FROM mooring_collab_documents WHERE page_id=?`, key.ArtifactID).Scan(&profile)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // The initializer will perform its ordinary empty-Page checks.
	}
	if err != nil {
		return err
	}
	if profile != LegacyCollaborationProfile {
		return ErrInvalid
	}
	return nil
}
