package pipeline

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	oldID := id
	id = PublicWaitpointID(workspaceID, oldID)
	if id != oldID {
		if err := rewriteLegacyWaitpoints(ctx, tx, nil, []legacyWaitpoint{{old: oldID, id: id, workspace: workspaceID}}); err != nil {
			// Constraint diagnostics may include the pre-conversion identifier.
			return "", false, errors.New("waitpoints: callback identity conversion failed")
		}
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
	if id != oldID {
		// Serialize the rename with a decision delivered immediately after COMMIT.
		s.mu.Lock()
		err = tx.Commit()
		if err == nil {
			s.moveLegacyListenerLocked(oldID, id)
		}
		s.mu.Unlock()
	} else {
		err = tx.Commit()
	}
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

// legacyWaitpointPredicate selects rows whose token is not a public id. It is
// written as a range over the primary key — every "wp_…" value sorts in
// ['wp_', 'wp`') because '`' follows '_' — so the per-sweep check is two index
// probes, not a table scan.
const legacyWaitpointPredicate = `(token < 'wp_' OR token >= 'wp` + "`" + `')`

// LegacyWaitpointPublicID converts a legacy identity to the same public ID
// during boot and restore. The space-separated domain is private to this
// identity transform. The old bearer token cannot open a callback.
func LegacyWaitpointPublicID(workspaceID, old string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("crewship waitpoint identity v1 %d:%s%s", len(workspaceID), workspaceID, old)))
	return WaitpointIDPrefix + hex.EncodeToString(sum[:16])
}

// PublicWaitpointID returns the durable identity used by boot conversion,
// notification receipts and scoped exports even while a row awaits migration.
func PublicWaitpointID(workspace, id string) string {
	if id == "" || strings.HasPrefix(id, WaitpointIDPrefix) {
		return id
	}
	return LegacyWaitpointPublicID(workspace, id)
}

var legacyWaitpointRefTables = []struct{ table, stmt string }{
	{"inbox_item_reads", `UPDATE inbox_item_reads AS r SET inbox_item_id = replace(r.inbox_item_id, l.old, l.id)
 FROM legacy AS l JOIN inbox_items AS i ON i.workspace_id = l.workspace AND i.source_id = l.old AND i.kind = 'waitpoint'
 WHERE r.inbox_item_id = i.id`},
	{"inbox_items", `UPDATE inbox_items AS i SET id = replace(i.id, l.old, l.id), source_id = l.id FROM legacy AS l
 WHERE i.workspace_id = l.workspace AND i.source_id = l.old AND i.kind = 'waitpoint'`},
	{"notification_deliveries", `UPDATE notification_deliveries AS n SET source_id = l.id, dedup_key = replace(n.dedup_key, l.old, l.id)
 FROM legacy AS l WHERE n.workspace_id = l.workspace AND n.source_id = l.old AND n.source_kind = 'waitpoint'`},
	{"audit_logs", `UPDATE audit_logs AS a SET entity_id = l.id FROM legacy AS l
 WHERE a.workspace_id = l.workspace AND a.entity_id = l.old AND a.entity_type = 'waitpoint'`},
}

type legacyWaitpoint struct{ old, id, workspace string }

func migrateLegacyWaitpointTokens(ctx context.Context, db *sql.DB, onMigrated func(old, id string)) (migrated, remaining int, err error) {
	rows, err := db.QueryContext(ctx, `SELECT token, workspace_id FROM pipeline_waitpoints WHERE `+
		legacyWaitpointPredicate+` ORDER BY status <> 'pending'`)
	if err != nil {
		return 0, 0, err
	}
	var todo []legacyWaitpoint
	for rows.Next() {
		var l legacyWaitpoint
		if err := rows.Scan(&l.old, &l.workspace); err != nil {
			rows.Close()
			return 0, 0, err
		}
		l.id = LegacyWaitpointPublicID(l.workspace, l.old)
		todo = append(todo, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(todo) == 0 {
		return 0, 0, err
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

	// A malformed row must not stall an otherwise healthy batch. Split only
	// failed batches and preserve per-row rollback for the exceptional case.
	var migrate func([]legacyWaitpoint)
	migrate = func(batch []legacyWaitpoint) {
		if ctx.Err() != nil {
			remaining += len(batch)
			return
		}
		if err := migrateLegacyWaitpointBatch(ctx, db, present, batch); err != nil {
			if len(batch) > 1 && ctx.Err() == nil {
				mid := len(batch) / 2
				migrate(batch[:mid])
				migrate(batch[mid:])
				return
			}
			remaining += len(batch)
			// A SQLite constraint error can contain the sensitive row value.
			if ctx.Err() == nil {
				slog.Error("waitpoint tokens: legacy row deferred for retry",
					"workspace_id", batch[0].workspace, "token", tokenDigestFingerprint(batch[0].old))
			}
			return
		}
		migrated += len(batch)
		if onMigrated != nil {
			for _, l := range batch {
				onMigrated(l.old, l.id)
			}
		}
	}
	const batchSize = 128
	for len(todo) > 0 {
		end := min(batchSize, len(todo))
		migrate(todo[:end])
		todo = todo[end:]
	}
	return migrated, remaining, nil
}

func migrateLegacyWaitpointBatch(ctx context.Context, db *sql.DB, present map[string]bool, batch []legacyWaitpoint) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	if err := rewriteLegacyWaitpoints(ctx, conn, present, batch); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func rewriteLegacyWaitpoints(ctx context.Context, tx DecisionDBTX, present map[string]bool, batch []legacyWaitpoint) error {
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return err
	}

	var args []any
	for _, l := range batch {
		args = append(args, l.old, l.id, l.workspace)
	}
	cte := "WITH legacy(old, id, workspace) AS (VALUES " + strings.Repeat("(?,?,?),", len(batch)-1) + "(?,?,?)) "
	if _, err := tx.ExecContext(ctx, cte+`UPDATE pipeline_waitpoints AS w SET token = l.id, callback_token_hash = NULL
 FROM legacy AS l WHERE w.token = l.old AND w.workspace_id = l.workspace`, args...); err != nil {
		return err
	}
	for _, ref := range legacyWaitpointRefTables {
		if present == nil || present[ref.table] {
			if _, err := tx.ExecContext(ctx, cte+ref.stmt, args...); err != nil {
				return fmt.Errorf("%s: %w", ref.table, err)
			}
		}
	}
	return nil
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

// migrateLegacyTokens is the store's conversion pass (construction and every
// sweep). Normally it costs two index probes and finds nothing. A converted
// row's in-memory listener, if any, follows it to the new id, so a waiter
// parked on the old value is still woken by a decision on the new one.
func (s *SQLWaitpointStore) migrateLegacyTokens() {
	ctx, cancel := context.WithTimeout(s.sweepContext(), 30*time.Second)
	defer cancel()
	migrated, remaining, err := migrateLegacyWaitpointTokens(ctx, s.db, s.moveLegacyListener)
	switch {
	case err != nil:
		slog.Error("waitpoint tokens: legacy migration failed", "migrated", migrated, "error", err)
	case migrated > 0:
		slog.Warn("waitpoint tokens: legacy waitpoints given public ids; their old callback URLs no longer work — mint new ones with `crewship routine waitpoints callback-url`",
			"rows", migrated, "remaining", remaining)
	}
}

func (s *SQLWaitpointStore) moveLegacyListener(old, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.moveLegacyListenerLocked(old, id)
}

func (s *SQLWaitpointStore) moveLegacyListenerLocked(old, id string) {
	if ch, ok := s.listeners[old]; ok {
		delete(s.listeners, old)
		s.listeners[id] = ch
	}
}

// tokenDigestFingerprint is a log-safe handle for a legacy (secret) token.
func tokenDigestFingerprint(token string) string {
	d := HashCapabilityToken(token)
	if len(d) > 16 {
		return d[:16]
	}
	return d
}
