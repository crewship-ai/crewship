package providerpool

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// Observation must come from a provider response or the native CLI's error
// envelope, never arbitrary tool output containing words such as "429".
// A temporary rate limit has a deadline; billing/authentication blocks require
// an explicit clear after intervention. This says nothing about quota usage
// percentages or whether another key shares the same project-level limit.
type Observation struct {
	// Generation must be captured with the encrypted access material used
	// for the request. A late old-token failure must not block a new login.
	// Write-only provenance: it is not persisted or populated on reads.
	Generation string
	// Revision is set by ReadObservation, not supplied by the event producer.
	Revision      int64
	At            time.Time
	CooldownUntil time.Time
	BlockedReason string
	Source        string
}

const observationTimeLayout = "2006-01-02T15:04:05.000000000Z"

var ErrSuperseded = errors.New("provider observation belongs to replaced access material")

// Generation fingerprints encrypted access material without decrypting it.
// It is provenance metadata, not an authentication credential. Delivery must
// carry it from the same snapshot as the value sent to the provider.
func Generation(encryptedValue string) string {
	sum := sha256.Sum256([]byte(encryptedValue))
	return hex.EncodeToString(sum[:])
}

func (s *Store) RecordObservation(ctx context.Context, workspaceID, credentialID string, observation Observation) error {
	if workspaceID == "" || credentialID == "" || observation.Generation == "" || observation.At.IsZero() ||
		(observation.Source != "provider_http" && observation.Source != "native_cli") ||
		(observation.BlockedReason != "" && observation.BlockedReason != "billing" && observation.BlockedReason != "authentication") ||
		(observation.CooldownUntil.IsZero() && observation.BlockedReason == "") ||
		(!observation.CooldownUntil.IsZero() && !observation.CooldownUntil.After(observation.At)) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var provider, kind, encryptedValue string
	err = tx.QueryRowContext(ctx, `SELECT provider, type, encrypted_value FROM credentials WHERE id = ? AND workspace_id = ?
		AND deleted_at IS NULL`, credentialID, workspaceID).Scan(&provider, &kind, &encryptedValue)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !providerlogin.IsProvider(provider) || (kind != "PROVIDER_LOGIN" && kind != "API_KEY" && kind != "AI_CLI_TOKEN") {
		return ErrInvalid
	}
	// This comparison and the write share the same immediate transaction.
	// Refresh/re-import cannot replace the access material between them.
	if Generation(encryptedValue) != observation.Generation {
		return ErrSuperseded
	}
	var cooldown, blocked any
	if !observation.CooldownUntil.IsZero() {
		cooldown = observation.CooldownUntil.UTC().Format(observationTimeLayout)
	}
	if observation.BlockedReason != "" {
		blocked = observation.BlockedReason
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_login_availability
		(credential_id, observed_at, cooldown_until, blocked_reason, source) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(credential_id) DO UPDATE SET
		  revision = provider_login_availability.revision + 1,
		  observed_at = excluded.observed_at,
		  cooldown_until = CASE
		    WHEN provider_login_availability.cooldown_until IS NULL THEN excluded.cooldown_until
		    WHEN excluded.cooldown_until IS NULL THEN provider_login_availability.cooldown_until
		    WHEN excluded.cooldown_until > provider_login_availability.cooldown_until
		      THEN excluded.cooldown_until ELSE provider_login_availability.cooldown_until END,
		  blocked_reason = COALESCE(excluded.blocked_reason, provider_login_availability.blocked_reason),
		  source = excluded.source
		WHERE excluded.observed_at >= provider_login_availability.observed_at`, credentialID,
		observation.At.UTC().Format(observationTimeLayout), cooldown, blocked, observation.Source)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ClearObservation is compare-and-clear, for a verified re-login/intervention.
// A successful old request must not erase a newer rate limit or auth failure.
// Capture the revision BEFORE the verification starts; fetching a fresh
// revision to clear an old verification result would defeat this guard.
// False means nothing matched, including a different workspace; it reveals no
// cross-tenant existence information. Mere metadata reads never call this.
func (s *Store) ClearObservation(ctx context.Context, workspaceID, credentialID string, revision int64) (bool, error) {
	if workspaceID == "" || credentialID == "" || revision <= 0 {
		return false, ErrInvalid
	}
	// Keep a tombstone so an old revision cannot match a newly inserted row
	// after clear (the ABA problem). No observation is physically deleted here.
	result, err := s.db.ExecContext(ctx, `UPDATE provider_login_availability
		SET cooldown_until = NULL, blocked_reason = NULL, revision = revision + 1
		WHERE credential_id = ? AND revision = ?
		AND EXISTS (SELECT 1 FROM credentials c WHERE c.id = credential_id AND c.workspace_id = ? AND c.deleted_at IS NULL)`,
		credentialID, revision, workspaceID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ReadObservation returns a tenant-scoped, metadata-only observation and its
// revision for a subsequent compare-and-clear. A revision distinguishes two
// concurrent provider events even when their timestamps are identical.
func (s *Store) ReadObservation(ctx context.Context, workspaceID, credentialID string) (Observation, error) {
	if workspaceID == "" || credentialID == "" {
		return Observation{}, ErrInvalid
	}
	var observation Observation
	var at, cooldown string
	err := s.db.QueryRowContext(ctx, `SELECT a.revision, a.observed_at, COALESCE(a.cooldown_until, ''),
		COALESCE(a.blocked_reason, ''), a.source FROM provider_login_availability a
		JOIN credentials c ON c.id = a.credential_id
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`, credentialID, workspaceID).
		Scan(&observation.Revision, &at, &cooldown, &observation.BlockedReason, &observation.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return Observation{}, ErrNotFound
	}
	if err != nil {
		return Observation{}, err
	}
	observation.At, err = time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Observation{}, ErrInvalid
	}
	if cooldown != "" {
		observation.CooldownUntil, err = time.Parse(time.RFC3339Nano, cooldown)
		if err != nil {
			return Observation{}, ErrInvalid
		}
	}
	return observation, nil
}
