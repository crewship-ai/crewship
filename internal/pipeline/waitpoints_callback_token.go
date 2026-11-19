package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Waitpoint identity vs. callback capability (#1910)
//
// pipeline_waitpoints.token used to be both the row's identity (primary key,
// inbox_items.source_id, a WAITING run's waitpoint_token, the authed approve
// path segment) AND the bearer capability for the unauthenticated public
// callback POST /api/v1/waitpoint-tokens/{token}. Because it was the identity,
// every member surface displayed it and the database held it in the clear, so
// reading either was enough to approve the gate.
//
// The two are now separate:
//
//   - pipeline_waitpoints.token is a PUBLIC, non-secret id: "wp_" + 32 hex.
//     The column keeps its name so every internal lookup is unchanged; it is
//     no longer a credential anywhere.
//   - The callback capability is "wpt_" + 64 hex, minted on demand by a
//     MANAGER+ (MintCallbackToken), shown once, and stored only as
//     callback_token_hash = HashCapabilityToken(secret) — the same unkeyed
//     "sh1:" SHA-256 scheme #1888 uses for webhooks and port exposures (see
//     webhooks.go for why it is not an HMAC).
//
// A waitpoint gets no callback capability when it is created: nobody is there
// to be shown it, and a waitpoint nobody minted a URL for simply cannot be
// completed without authentication.
// ---------------------------------------------------------------------------

const (
	// WaitpointIDPrefix marks a public waitpoint id. Legacy rows (pre-#1910)
	// hold a bare-hex secret, a backup fork's "tok_…" or a seed's "seed_…"
	// value instead, which is how the migration below recognises them.
	WaitpointIDPrefix = "wp_"
	// waitpointCallbackSecretPrefix marks a callback secret, so it is
	// recognisable in a leaked log line or a pasted URL.
	waitpointCallbackSecretPrefix = "wpt_"
)

// ErrWaitpointNotFound is returned when a waitpoint id does not name a
// waitpoint in the caller's workspace.
var ErrWaitpointNotFound = errors.New("waitpoint not found")

// ErrCallbackNotOffered is returned by MintCallbackToken for a waitpoint the
// public callback could never complete: a typed decision (decision_form) or
// a held crewship action. Decide refuses the external decider on both, so a
// URL for them would be a capability that cannot be exercised.
var ErrCallbackNotOffered = errors.New("waitpoint: this waitpoint can only be decided by an authenticated person, so it has no callback URL")

// newWaitpointID mints a public waitpoint id. It is not a secret, but it is
// still random: ids must not collide and must not be guessable into some
// other workspace's row (every lookup is workspace-scoped regardless).
func newWaitpointID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("waitpoints: mint id: %w", err)
	}
	return WaitpointIDPrefix + hex.EncodeToString(b), nil
}

// newWaitpointCallbackSecret mints a callback capability: 32 bytes of
// crypto/rand, the webhook token's width.
func newWaitpointCallbackSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("waitpoints: mint callback secret: %w", err)
	}
	return waitpointCallbackSecretPrefix + hex.EncodeToString(b), nil
}

// CallbackOffered reports whether the public callback can complete a
// waitpoint with this step id and decision form. It mirrors the two refusals
// in Decide's external branch (held action, typed decision) so the list
// endpoint, the mint endpoint and the door agree.
func CallbackOffered(stepID, decisionFormJSON string) bool {
	return decisionFormJSON == "" && !IsCrewshipApprovalStepKey(stepID)
}

// MintCallbackToken mints a fresh callback secret for a PENDING waitpoint in
// workspaceID, stores only its digest and returns the secret — the one and
// only time it exists outside the caller. A previous secret is replaced
// (rotated reports that), so re-minting is also how a leaked URL is revoked.
func (s *SQLWaitpointStore) MintCallbackToken(ctx context.Context, workspaceID, id string) (secret string, rotated bool, err error) {
	if workspaceID == "" || id == "" {
		return "", false, ErrWaitpointNotFound
	}
	var status, stepID, form, prior string
	err = s.db.QueryRowContext(ctx, `
SELECT status, step_id, decision_form_json, COALESCE(callback_token_hash, '')
  FROM pipeline_waitpoints WHERE workspace_id = ? AND token = ? AND kind = 'approval'`,
		workspaceID, id).Scan(&status, &stepID, &form, &prior)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrWaitpointNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("waitpoints: mint lookup: %w", err)
	}
	if !CallbackOffered(stepID, form) {
		return "", false, ErrCallbackNotOffered
	}
	if status != "pending" {
		return "", false, ErrAlreadyDecided
	}
	secret, err = newWaitpointCallbackSecret()
	if err != nil {
		return "", false, err
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE pipeline_waitpoints SET callback_token_hash = ?
 WHERE workspace_id = ? AND token = ? AND status = 'pending'`,
		HashCapabilityToken(secret), workspaceID, id)
	if err != nil {
		return "", false, fmt.Errorf("waitpoints: store callback digest: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Decided between the read and the write.
		return "", false, ErrAlreadyDecided
	}
	return secret, prior != "", nil
}

// ResolveCallbackToken maps a value presented at the public callback to the
// waitpoint it authorises: (id, workspaceID), or ("", "", nil) when it
// authorises nothing. The presented value is hashed and the digest looked up;
// the cleartext is never compared and never stored. A stored digest, a
// redaction marker or a public waitpoint id all resolve to nothing.
func (s *SQLWaitpointStore) ResolveCallbackToken(ctx context.Context, presented string) (id, workspaceID string, err error) {
	digest := CapabilityLookupDigest(presented)
	if digest == "" {
		return "", "", nil
	}
	var stored string
	err = s.db.QueryRowContext(ctx, `
SELECT token, workspace_id, callback_token_hash FROM pipeline_waitpoints WHERE callback_token_hash = ?`,
		digest).Scan(&id, &workspaceID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("waitpoints: resolve callback token: %w", err)
	}
	// Constant-time confirmation, so the decision never rests on a
	// short-circuiting byte comparison (same as WebhookStore.getByDigest).
	if !CapabilityDigestEqual(stored, digest) {
		return "", "", nil
	}
	return id, workspaceID, nil
}

// ResolveWaitpointRef maps what an AUTHENTICATED caller put in the approve
// path to a waitpoint id in their workspace. Normally that is the id itself.
// A pre-#1910 token value (a script that captured `waitpoint_token` before
// the upgrade) is no longer an id, but its digest is still the row's callback
// digest, so it is resolved through that — scoped to the caller's workspace,
// so it grants nothing the authenticated route did not already allow. Anything
// else is returned unchanged and the decide path answers it as before.
func (s *SQLWaitpointStore) ResolveWaitpointRef(ctx context.Context, workspaceID, ref string) string {
	if ref == "" || strings.HasPrefix(ref, WaitpointIDPrefix) {
		return ref
	}
	digest := CapabilityLookupDigest(ref)
	if digest == "" {
		return ref
	}
	var id string
	if err := s.db.QueryRowContext(ctx,
		`SELECT token FROM pipeline_waitpoints WHERE workspace_id = ? AND callback_token_hash = ?`,
		workspaceID, digest).Scan(&id); err != nil {
		return ref
	}
	return id
}

// ---------------------------------------------------------------------------
// Legacy-row migration
//
// The SQL half (migrations/20261119153114_waitpoint_callback_token_hash.sql)
// adds callback_token_hash; SQLite has no SHA-256, so the rest happens here,
// when the store is constructed — before anything serves a request — and is
// retried by the sweeper for any row that failed. For each row whose token is
// not a public id, in ONE transaction:
//
//   - callback_token_hash = HashCapabilityToken(old token), IN PLACE, so an
//     external system already holding a callback URL keeps working;
//   - token = a fresh public id, so the cleartext leaves the row;
//   - every copy of the old value is rewritten to the new id:
//     inbox_items.source_id, notification_deliveries.source_id + dedup_key,
//     audit_logs.entity_id.
//
// A row that fails stays exactly as it was (the transaction rolls back): the
// authed routes still find it by its old value, the public callback does not
// (fail closed), and the next sweep tries again.
// ---------------------------------------------------------------------------

// legacyWaitpointRefTables are the copies of a waitpoint's identity outside
// its own row. Each statement binds ?1 = new id, ?2 = old value, ?3 = the
// row's workspace.
var legacyWaitpointRefTables = []struct{ table, stmt string }{
	{"inbox_items", `UPDATE inbox_items SET source_id = ?1 WHERE source_id = ?2 AND workspace_id = ?3 AND kind = 'waitpoint'`},
	// dedup_key is "<category>:<source_id>" (notifyroute.Router).
	{"notification_deliveries", `UPDATE notification_deliveries SET source_id = ?1, dedup_key = replace(dedup_key, ?2, ?1) WHERE source_id = ?2 AND workspace_id = ?3 AND source_kind = 'waitpoint'`},
	{"audit_logs", `UPDATE audit_logs SET entity_id = ?1 WHERE entity_id = ?2 AND workspace_id = ?3 AND entity_type = 'waitpoint'`},
}

// migrateLegacyWaitpointTokens converts every legacy row. Returns how many it
// converted and how many are still legacy afterwards.
func migrateLegacyWaitpointTokens(ctx context.Context, db *sql.DB) (migrated, remaining int, err error) {
	rows, err := db.QueryContext(ctx,
		`SELECT token, workspace_id FROM pipeline_waitpoints WHERE substr(token, 1, 3) <> ?`, WaitpointIDPrefix)
	if err != nil {
		return 0, 0, err
	}
	type legacy struct{ token, workspaceID string }
	var todo []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.token, &l.workspaceID); err != nil {
			rows.Close()
			return 0, 0, err
		}
		todo = append(todo, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, err
	}
	rows.Close()
	if len(todo) == 0 {
		return 0, 0, nil
	}

	present := map[string]bool{}
	for _, ref := range legacyWaitpointRefTables {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, ref.table).Scan(&n); err != nil {
			return 0, len(todo), err
		}
		present[ref.table] = n > 0
	}

	for i, l := range todo {
		if cerr := ctx.Err(); cerr != nil {
			return migrated, len(todo) - i, nil
		}
		if err := migrateLegacyWaitpointRow(ctx, db, present, l.token, l.workspaceID); err != nil {
			remaining++
			// The row id is the secret being migrated: log a fingerprint only.
			slog.Error("waitpoint tokens: migrating a legacy row failed — it is retried by the sweeper",
				"workspace_id", l.workspaceID, "token", tokenDigestFingerprint(l.token), "error", err)
			continue
		}
		migrated++
	}
	return migrated, remaining, nil
}

func migrateLegacyWaitpointRow(ctx context.Context, db *sql.DB, present map[string]bool, old, workspaceID string) error {
	id, err := newWaitpointID()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// An empty token cannot be presented at the callback, so it gets no
	// digest; HashCapabilityToken("") is "" and NULL is stored instead.
	var digest any
	if h := HashCapabilityToken(old); h != "" {
		digest = h
	}
	res, err := tx.ExecContext(ctx, `
UPDATE pipeline_waitpoints SET token = ?, callback_token_hash = COALESCE(callback_token_hash, ?)
 WHERE token = ? AND substr(token, 1, 3) <> ?`, id, digest, old, WaitpointIDPrefix)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // migrated concurrently
	}
	for _, ref := range legacyWaitpointRefTables {
		if !present[ref.table] {
			continue
		}
		if _, err := tx.ExecContext(ctx, ref.stmt, id, old, workspaceID); err != nil {
			return fmt.Errorf("%s: %w", ref.table, err)
		}
	}
	return tx.Commit()
}

// prepareWaitpointTokens is the constructor-time sequence: make sure the
// digest column exists, then migrate legacy rows. Returns how many rows are
// still legacy; never fatal — a store that refuses to construct would take
// the daemon down, and an unmigrated row fails closed at the public callback.
func prepareWaitpointTokens(db *sql.DB) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureCapabilityDigestColumn(ctx, db, "pipeline_waitpoints", "callback_token_hash"); err != nil {
		slog.Warn("waitpoint tokens: ensure callback_token_hash column", "error", err)
		return 1
	}
	migrated, remaining, err := migrateLegacyWaitpointTokens(ctx, db)
	if err != nil {
		slog.Error("waitpoint tokens: legacy migration failed", "migrated", migrated, "error", err)
		return 1
	}
	if migrated > 0 {
		slog.Info("waitpoint tokens: legacy rows given public ids, callback tokens hashed at rest", "rows", migrated)
	}
	return remaining
}

// tokenDigestFingerprint is a log-safe handle for a legacy (secret) token.
func tokenDigestFingerprint(token string) string {
	d := HashCapabilityToken(token)
	if len(d) > 16 {
		return d[:16]
	}
	return d
}
