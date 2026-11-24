package pipeline

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/database"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
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
func (s *SQLWaitpointStore) MintCallbackToken(ctx context.Context, workspaceID, id, actorID string) (secret string, rotated bool, err error) {
	if workspaceID == "" || id == "" {
		return "", false, ErrWaitpointNotFound
	}
	if actorID == "" {
		return "", false, errors.New("waitpoints: missing mint actor")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	// Reserve the SQLite writer before reading the previous digest, including
	// on connections configured with deferred transactions.
	if _, err := tx.ExecContext(ctx, `UPDATE pipeline_waitpoints SET callback_token_hash=NULL WHERE 0`); err != nil {
		return "", false, err
	}
	var status, stepID, form, prior string
	err = tx.QueryRowContext(ctx, `
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
	res, err := tx.ExecContext(ctx, `
UPDATE pipeline_waitpoints SET callback_token_hash = ?
 WHERE workspace_id = ? AND token = ? AND status = 'pending'`,
		HashCapabilityToken(secret), workspaceID, id)
	if err != nil {
		return "", false, fmt.Errorf("waitpoints: store callback digest: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return "", false, errors.Join(ErrAlreadyDecided, err)
	}
	rotated = prior != ""
	res, err = tx.ExecContext(ctx, `INSERT INTO audit_logs
		(id, workspace_id, user_id, action, entity_type, entity_id, metadata, created_at)
		VALUES (lower(hex(randomblob(16))), ?, ?, 'waitpoint.callback_token.minted', 'waitpoint', ?, ?, ?)`,
		workspaceID, actorID, id, fmt.Sprintf(`{"rotated":%t}`, rotated), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return "", false, fmt.Errorf("waitpoints: audit callback mint: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return "", false, errors.Join(errors.New("waitpoints: missing callback mint audit"), err)
	}
	entry := journal.Entry{
		WorkspaceID: workspaceID, ActorType: journal.ActorUser, ActorID: actorID,
		Type: journal.EntryAuditEntityUpdated, Severity: journal.SeverityNotice,
		Summary: "Waitpoint callback URL minted",
		Payload: map[string]any{"action": "waitpoint.callback_token.minted", "rotated": rotated},
		Refs:    map[string]any{"entity_type": "waitpoint", "entity_id": id},
	}
	var notify func()
	if s.auditJournal != nil {
		notify, err = s.auditJournal.AppendTx(ctx, tx, entry)
	} else {
		notify, err = journal.AppendTx(ctx, tx, entry)
	}
	if err != nil {
		return "", false, err
	}
	err = tx.Commit()
	if err != nil {
		return "", false, err
	}
	if notify != nil {
		notify()
	}
	return secret, rotated, nil
}

// ResolveCallbackToken maps a value presented at the public callback to the
// waitpoint it authorises: (id, workspaceID), or ("", "", nil) when it
// authorises nothing. The presented value is hashed and the digest looked up;
// the cleartext is never compared and never stored. A stored digest, a
// redaction marker or a public waitpoint id all resolve to nothing.
func (s *SQLWaitpointStore) ResolveCallbackToken(ctx context.Context, presented string) (id, workspaceID string, err error) {
	return resolveCallbackToken(ctx, s.db, presented)
}

func resolveCallbackToken(ctx context.Context, tx DecisionDBTX, presented string) (id, workspaceID string, err error) {
	digest := CapabilityLookupDigest(presented)
	if digest == "" {
		return "", "", nil
	}
	var stored string
	err = tx.QueryRowContext(ctx, `
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

// DecideCallback authenticates the grant while holding the same SQLite writer
// reservation as rotation and the final decision. Only committed decisions notify listeners.
func (s *SQLWaitpointStore) DecideCallback(ctx context.Context, presented string, approved bool, payload string) (id, workspaceID string, err error) {
	// Reject unknown grants without reserving a database writer. The writer
	// repeats the digest lookup below before authorizing any state change.
	if candidate, _, err := s.ResolveCallbackToken(ctx, presented); err != nil {
		return "", "", err
	} else if candidate == "" {
		return "", "", ErrAlreadyDecided
	}
	decider := WaitpointDecider{Kind: DeciderExternal, ID: "external-callback"}
	err = s.commitDecision(ctx, func(tx *sql.Conn) (*WaitpointDecision, error) {
		var lookupErr error
		id, workspaceID, lookupErr = resolveCallbackToken(ctx, tx, presented)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if id == "" {
			return nil, ErrAlreadyDecided
		}
		return s.DecideTx(ctx, tx, workspaceID, id, approved, decider, payload)
	})
	if errors.Is(err, ErrDeciderNotAllowed) {
		reason := refusalReasonDecider
		if errors.Is(err, ErrHeldActionDeciderNotAllowed) {
			reason = refusalReasonHeldAction
		}
		s.recordRefusal(ctx, workspaceID, id, approved, decider, reason)
	}
	return id, workspaceID, err
}

// ---------------------------------------------------------------------------
// Legacy-row migration
//
// The SQL half (migrations/20261119180903_waitpoint_callback_token_hash.sql)
// adds callback_token_hash. The rest runs in Go when the store is constructed
// — before anything serves a request — and again on every sweep, so a row
// that failed, or a pre-#1910 row a restore brought back into a running
// server, is converted within one sweep interval.
//
// OLD SECRETS ARE NOT CARRIED FORWARD. Before #1910 every workspace member
// could read a working callback URL from GET …/pipelines/waitpoints, and the
// token also sat in the clear in several other tables, so a pre-upgrade secret
// cannot be presumed private. A legacy row therefore gets NO digest: the
// public callback refuses it until a MANAGER mints a new URL
// (`crewship routine waitpoints callback-url`). The authenticated routes keep
// working because they key on the new id.
//
// For each row whose token is not a public id, in ONE transaction, the old
// value is replaced by a fresh public id in the following identity references. Historical ADMIN CLI request paths
// can retain the invalidated token:
//
//   - pipeline_waitpoints.token;
//   - inbox_items.source_id AND inbox_items.id, which the inbox writer derives
//     as "ibx_<workspace>_waitpoint_<source_id>" (internal/inbox/writer.go);
//   - inbox_item_reads.inbox_item_id, the one foreign key onto inbox_items.id;
//   - notification_deliveries.source_id and dedup_key ("<category>:<source>");
//   - audit_logs.entity_id for waitpoint refusals.
//
// A row that fails stays exactly as it was (the transaction rolls back) and
// is retried on the next sweep.
// ---------------------------------------------------------------------------

// LegacyWaitpointPublicID converts a legacy identity to the same public ID
// during boot and restore. The space-separated domain is private to this
// identity transform. The old bearer token cannot open a callback.
func LegacyWaitpointPublicID(workspaceID, old string) string {
	return database.LegacyWaitpointPublicID(workspaceID, old)
}

// PublicWaitpointID returns the durable identity used by boot conversion,
// notification receipts and scoped exports even while a row awaits migration.
func PublicWaitpointID(workspace, id string) string {
	if id == "" || strings.HasPrefix(id, WaitpointIDPrefix) {
		return id
	}
	return LegacyWaitpointPublicID(workspace, id)
}

// ensureWaitpointDigestColumn makes sure callback_token_hash exists (hand-rolled
// test schemas and partial restores lack the migration).
func ensureWaitpointDigestColumn(db *sql.DB) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ensureCapabilityDigestColumn(ctx, db, "pipeline_waitpoints", "callback_token_hash"); err != nil {
		slog.Warn("waitpoint tokens: ensure callback_token_hash column", "error", err)
	}
}
