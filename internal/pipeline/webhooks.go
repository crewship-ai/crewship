package pipeline

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// ---------------------------------------------------------------------------
// Capability-token hashing (#1888)
//
// Two tokens in this schema are self-contained capabilities — port_exposures
// (`/exposed/{token}/…`) and pipeline_webhooks
// (`POST /api/v1/webhooks/{token}`). Neither endpoint authenticates: holding
// the token IS the authorization. Storing them in cleartext meant a leaked
// backup, a copied `.db`, or any read primitive handed the reader live,
// working URLs.
//
// (pipeline_waitpoints.token is the same kind of secret and is deliberately
// NOT covered: it is that table's primary key, the handle carried by
// inbox_items.source_id and a WAITING run's waitpoint_token, and the value
// GET …/pipelines/waitpoints reads back out of the column to rebuild the
// public callback URL on demand. It is a retrievable shared secret by
// contract, so hashing it means redesigning that contract first.)
//
// cli_tokens has solved this since Patch J and the shape is copied from there:
// keep a digest, look the digest up, never read the cleartext back.
//
// WHY THE DIGEST IS UNKEYED SHA-256, AND NOT AN HMAC. An HMAC key protects a
// digest whose *input space is small*: passwords, sequential ids, anything an
// attacker holding the database file could enumerate and hash offline. That is
// not the shape of these two secrets. Both are 32 bytes straight out of
// crypto/rand — generateWebhookToken here ("wh_" + 64 hex chars) and
// generateExposeToken in internal/api/port_expose_handler.go (43 chars of
// base64url) — so a preimage search is 2^256 wide. Against an input that
// large, unkeyed SHA-256 is exactly as unbreakable as HMAC-SHA256; the key
// buys no security it did not already have.
//
// What the key DID buy was a key-lifecycle problem that could brick the
// instance. The previous revision derived an HMAC subkey from the raw
// ENCRYPTION_KEY env var. Crewship documents a master-key rotation
// (CREWSHIP_ENCRYPTION_KEY_VERSION + ENCRYPTION_KEY_V2 + POST /admin/reencrypt,
// see internal/api/admin_reencrypt.go and internal/secrets/bootstrap.go) whose
// final step retires ENCRYPTION_KEY. After that step every presented token
// would hash under a different scheme than every stored digest — and because
// the cleartext column has already been overwritten with `redacted:<id>`,
// nothing could be recovered. Every webhook sender and every published
// /exposed/ URL would 404, permanently. Trading that failure mode for zero
// additional security was the wrong trade, so there is now one scheme, keyed
// by nothing, that no rotation can invalidate.
//
// The scheme prefix stays: it is what makes a stored digest recognisable as a
// digest (so replaying one read out of the database resolves to nothing) and
// it leaves room for a future algorithm change to add a prefix rather than
// rewrite one.
const (
	// capabilityDigestScheme prefixes every at-rest capability digest.
	// SHA-256 over the token, hex-encoded.
	capabilityDigestScheme = "sh1:"
)

// HashCapabilityToken returns the digest to STORE for a capability token.
// An empty token has no digest.
func HashCapabilityToken(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return capabilityDigestScheme + hex.EncodeToString(sum[:])
}

// CapabilityLookupDigest maps a value presented at a public endpoint to the
// single digest it may legally match at rest, or "" when the value cannot be a
// capability token at all. Callers must treat "" as "no match" rather than as
// an unfiltered query.
//
// Presenting a stored digest is NOT presenting the token, and neither is
// presenting a redaction marker (which is derived from a row id, i.e. from a
// value the caller may well know): both resolve to nothing.
func CapabilityLookupDigest(token string) string {
	if token == "" || IsCapabilityTokenDigest(token) || strings.HasPrefix(token, redactedTokenPrefix) {
		return ""
	}
	return HashCapabilityToken(token)
}

// IsCapabilityTokenDigest reports whether s is an at-rest digest rather than a
// cleartext capability token. Real tokens are bare hex (`wh_`-prefixed for
// webhooks) or base64url, so the scheme prefix is unambiguous.
func IsCapabilityTokenDigest(s string) bool {
	return strings.HasPrefix(s, capabilityDigestScheme)
}

// CapabilityDigestEqual compares two digests without leaking, through timing,
// how many leading bytes matched.
func CapabilityDigestEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

// ensureTokenHashColumn adds the token_hash column when it is missing.
//
// The 20260810171000 migration is what adds it in production. This guard
// exists because both stores are also constructed against databases the
// migration runner never touched — the package's own tests hand-roll these
// tables, and so does anything that restores a partial dump — and a store that
// cannot find its lookup column would fail every capability check closed
// rather than degrade. Idempotent and cheap: one PRAGMA per store lifetime.
func ensureTokenHashColumn(ctx context.Context, db *sql.DB, table string) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := false
	present := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		found = true
		if name == "token_hash" {
			present = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !found || present {
		// !found = the table does not exist in this database at all; that
		// is not this helper's problem to report.
		return nil
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN token_hash TEXT`); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_`+table+`_token_hash ON `+table+` (token_hash) WHERE token_hash IS NOT NULL`)
	return err
}

// backfillTokenHashes hashes every row of pipeline_webhooks that still carries
// a cleartext token and overwrites the cleartext in place. Returns how many
// rows it converted and how many it could not.
//
// This is the half of the 20260810171000 migration that SQL cannot express:
// SQLite has no SHA-256. The .sql file adds the column and the index; the
// digest has to be computed in Go, and it has to happen before anything serves
// traffic — hence the store constructor. It is idempotent (the predicate is
// "token_hash IS NULL") and normally reads zero rows.
//
// ONE FAILED ROW MUST NOT ABORT THE REST. This used to `return` on the first
// failed UPDATE. SQLite has a single writer, so a moment of lock contention
// part-way through the loop — exactly what a busy daemon produces — left every
// remaining row un-hashed, and the digest-only lookup path then 404'd webhooks
// whose tokens were still perfectly valid. So a failure is counted and logged
// per row and the loop continues; the caller uses the failure count to decide
// whether the bounded cleartext fallback in GetByToken needs to be armed.
//
// Soft-deleted rows are included deliberately: a deleted webhook's token is
// dead as a capability but is still a live secret sitting in a table, and
// nothing else would ever come back for it.
func backfillTokenHashes(ctx context.Context, db *sql.DB) (hashed, failed int, err error) {
	rows, qerr := db.QueryContext(ctx,
		`SELECT id, token FROM pipeline_webhooks WHERE token_hash IS NULL OR token_hash = ''`)
	if qerr != nil {
		return 0, 0, qerr
	}
	type pending struct{ id, token string }
	var todo []pending
	for rows.Next() {
		var p pending
		if serr := rows.Scan(&p.id, &p.token); serr != nil {
			rows.Close()
			return 0, 0, serr
		}
		todo = append(todo, p)
	}
	if rerr := rows.Err(); rerr != nil {
		rows.Close()
		return 0, 0, rerr
	}
	rows.Close()

	for i, p := range todo {
		if p.token == "" || strings.HasPrefix(p.token, redactedTokenPrefix) {
			// Nothing recoverable in this row's cleartext column. It is
			// not resolvable by any token, which is a dead row rather than
			// a hashing failure — but it is also not hashed, so it counts
			// as such for the caller's arming decision only if it could
			// still hold a live capability, which it cannot.
			continue
		}
		if cerr := ctx.Err(); cerr != nil {
			// Out of budget: the rest are still cleartext.
			failed += len(todo) - i
			slog.Warn("capability tokens: backfill ran out of time",
				"table", "pipeline_webhooks", "hashed", hashed, "remaining", failed, "error", cerr)
			return hashed, failed, nil
		}
		if _, uerr := db.ExecContext(ctx,
			`UPDATE pipeline_webhooks SET token_hash = ?, token = ? WHERE id = ?`,
			HashCapabilityToken(p.token), redactedWebhookToken(p.id), p.id,
		); uerr != nil {
			failed++
			slog.Error("capability tokens: hashing a row failed — it still holds cleartext",
				"table", "pipeline_webhooks", "id", p.id, "error", uerr)
			continue
		}
		hashed++
	}
	return hashed, failed, nil
}

// prepareCapabilityTokens is the constructor-time sequence: make sure the
// column exists, then hash whatever is still cleartext. Returns the number of
// rows left holding cleartext. Failures are logged rather than fatal — a store
// that refuses to construct would take the whole daemon down.
func prepareCapabilityTokens(db *sql.DB) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureTokenHashColumn(ctx, db, "pipeline_webhooks"); err != nil {
		slog.Warn("capability tokens: ensure token_hash column", "table", "pipeline_webhooks", "error", err)
		// The column may not exist, so no row can be hashed and no row can
		// be looked up by digest. Arm the cleartext fallback so already
		// published webhook URLs keep resolving.
		return 1
	}
	hashed, failed, err := backfillTokenHashes(ctx, db)
	if err != nil {
		slog.Error("capability tokens: backfill failed — rows still hold cleartext",
			"table", "pipeline_webhooks", "hashed", hashed, "error", err)
		return 1
	}
	if hashed > 0 {
		slog.Info("capability tokens hashed at rest", "table", "pipeline_webhooks", "rows", hashed)
	}
	if failed > 0 {
		slog.Error("capability tokens: rows still hold cleartext after the backfill",
			"table", "pipeline_webhooks", "hashed", hashed, "unhashed", failed,
			"note", "those webhooks resolve by cleartext until a later attempt hashes them")
	}
	return failed
}

// DefaultWebhookTimestampTolerance mirrors webhook.DefaultTimestampTolerance
// (internal/webhook/handler.go) — the agent-webhook path's Stripe/Svix
// ts.body scheme. #1416 item 2: pipeline webhooks get the same freshness
// window when a sender opts in via X-Crewship-Timestamp.
const DefaultWebhookTimestampTolerance = 5 * time.Minute

// WebhookUntrustedInputKeys names the inputs map keys FireWebhook derives
// directly from the raw request bytes (event payload, raw body, headers).
// The single source of truth for two independent hardening pieces:
//
//  1. internal/api/pipeline_webhooks.go's reservedWebhookInputKeys — an
//     operator-defined inputs_template may not override these (audit
//     A17.2 M2, confused-deputy).
//  2. #1416 item 1 — the executor fences these input values before they
//     reach an agent_run prompt on a webhook-triggered run (see
//     renderAgentPrompt).
//
// Keeping ONE set for both prevents the "template override" allowlist and
// the "needs fencing" allowlist from drifting apart.
var WebhookUntrustedInputKeys = map[string]struct{}{
	"event":   {},
	"raw":     {},
	"headers": {},
}

// Webhook is the persisted record for an event-driven trigger.
// Token-addressed: POST /api/v1/webhooks/{token} fires this pipeline
// with the request body delivered as the `event` input.
//
// Pinned to target_pipeline_id (not slug) so a rename keeps the
// webhook working — callers don't need to update their senders.
type Webhook struct {
	ID                    string
	WorkspaceID           string
	Name                  string
	TargetPipelineID      string
	TargetPipelineVersion *int
	// Token is the CLEARTEXT token and is populated only in memory: by
	// Save on the mint path (so the create response can show it once) and
	// by GetByToken, which echoes back the value the caller presented so
	// the handler's per-webhook rate-limit and idempotency keys stay
	// stable. It is never read back from the database — see TokenHash.
	Token string
	// TokenHash is the at-rest digest the dispatch path looks up.
	TokenHash          string
	SigningSecret      string // empty = no HMAC verification
	InputsTemplateJSON string
	Enabled            bool
	RateLimitPerMin    int
	LastFiredAt        *time.Time
	LastStatus         string
	LastRunID          string
	FireCount          int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// SaveWebhookInput is the payload for WebhookStore.Save.
type SaveWebhookInput struct {
	ID                    string // "" = create; non-empty = update
	WorkspaceID           string
	Name                  string
	TargetPipelineID      string
	TargetPipelineVersion *int
	SigningSecret         string
	InputsTemplate        map[string]any
	Enabled               bool
	RateLimitPerMin       int
}

// WebhookStore is the persistence + lookup layer for pipeline_webhooks.
type WebhookStore struct {
	db *sql.DB

	// unhashed counts the rows the constructor's backfill could not hash,
	// i.e. the rows that still hold a cleartext token. While it is >0
	// GetByToken arms a second, bounded lookup against the cleartext column
	// for tokens the digest index does not resolve; each such lookup that
	// succeeds re-attempts the hashing and decrements this. At 0 the hot
	// path is a single indexed equality and nothing ever reads `token`.
	unhashed atomic.Int64
}

// NewWebhookStore returns a store backed by a v82+ DB.
//
// Construction also completes the 20260810171000 migration for this table:
// the .sql file adds token_hash, and this hashes whatever cleartext is still
// sitting in `token` (see backfillTokenHashes for why that half cannot be
// SQL). Production constructs exactly one of these at boot, before the router
// is serving.
func NewWebhookStore(db *sql.DB) *WebhookStore {
	s := &WebhookStore{db: db}
	s.unhashed.Store(int64(prepareCapabilityTokens(db)))
	return s
}

// redactedTokenPrefix marks a cleartext capability column that has been
// overwritten. The columns are NOT NULL UNIQUE, so an empty string is not
// available for more than one row; deriving the marker from the primary key
// keeps it unique, obviously dead, and traceable back to the row it belonged
// to.
const redactedTokenPrefix = "redacted:"

// redactedWebhookToken is what replaces the cleartext in pipeline_webhooks.
func redactedWebhookToken(id string) string { return redactedTokenPrefix + id }

// Save creates or updates a webhook. On create, mints a fresh token;
// on update, the token is preserved (changing the token would break
// every existing sender — callers should delete + re-create if they
// want a new token).
func (s *WebhookStore) Save(ctx context.Context, in SaveWebhookInput) (*Webhook, error) {
	if in.WorkspaceID == "" || in.TargetPipelineID == "" {
		return nil, errors.New("pipeline_webhooks: workspace_id + target_pipeline_id required")
	}
	tmplJSON, err := json.Marshal(in.InputsTemplate)
	if err != nil {
		return nil, fmt.Errorf("marshal inputs_template: %w", err)
	}
	if string(tmplJSON) == "null" {
		tmplJSON = []byte("{}")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// #1029: encrypt the optional HMAC signing secret at rest (the v82 schema
	// already documents this column as encrypted, but writes stored plaintext).
	// #1254 item C: fail-CLOSED. With no usable key EncryptAtRest returns an
	// error and the save is rejected rather than silently storing the secret
	// in plaintext (opt back in with CREWSHIP_ALLOW_PLAINTEXT_SECRETS=true).
	// The read/verify path still decrypts only enveloped values, so rows
	// written in plaintext by older builds keep working.
	storedSigning, _, err := encryption.EncryptAtRest(in.SigningSecret)
	if err != nil {
		return nil, fmt.Errorf("encrypt signing_secret: %w", err)
	}

	if in.ID == "" {
		id := generateWebhookID()
		token, err := generateWebhookToken()
		if err != nil {
			return nil, fmt.Errorf("mint webhook token: %w", err)
		}
		_, err = s.db.ExecContext(ctx, `
INSERT INTO pipeline_webhooks (
    id, workspace_id, name, target_pipeline_id, target_pipeline_version,
    token, token_hash, signing_secret, inputs_template,
    enabled, rate_limit_per_min,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.WorkspaceID, in.Name, in.TargetPipelineID,
			nullInt(in.TargetPipelineVersion),
			// The cleartext never reaches the table (#1888): only its
			// digest does, and `token` gets the dead redaction marker
			// its NOT NULL UNIQUE constraint requires.
			redactedWebhookToken(id), HashCapabilityToken(token),
			nullStr(storedSigning), string(tmplJSON),
			boolToInt(in.Enabled), in.RateLimitPerMin,
			now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert webhook: %w", err)
		}
		created, err := s.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		// Show-once: the only moment a caller can see the cleartext.
		// Every later read returns an empty Token.
		created.Token = token
		return created, nil
	}

	_, err = s.db.ExecContext(ctx, `
UPDATE pipeline_webhooks
SET name = ?, target_pipeline_id = ?, target_pipeline_version = ?,
    signing_secret = ?, inputs_template = ?, enabled = ?, rate_limit_per_min = ?,
    updated_at = ?
WHERE id = ? AND deleted_at IS NULL`,
		in.Name, in.TargetPipelineID, nullInt(in.TargetPipelineVersion),
		nullStr(storedSigning), string(tmplJSON),
		boolToInt(in.Enabled), in.RateLimitPerMin, now, in.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("update webhook: %w", err)
	}
	return s.GetByID(ctx, in.ID)
}

// GetByID returns a webhook by id, or ErrNotFound.
func (s *WebhookStore) GetByID(ctx context.Context, id string) (*Webhook, error) {
	rows, err := s.db.QueryContext(ctx, webhookSelect+` WHERE id = ? AND deleted_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	return scanWebhook(rows)
}

// GetByToken resolves the public token segment to a webhook row.
// The token is what arrives in the URL path; this is the inbound
// dispatch path that the public webhook handler hits on every
// fire. Returns ErrNotFound for unknown tokens — handler maps to
// 404 (deliberately not 403; we don't want to leak which tokens
// exist via timing or status code differences).
func (s *WebhookStore) GetByToken(ctx context.Context, token string) (*Webhook, error) {
	// Hash what was presented and look the digest up (#1888). The cleartext
	// is not in the table to compare against for a hashed row, and
	// CapabilityLookupDigest refuses a value that is already a digest — so
	// replaying something read out of the database resolves to nothing.
	digest := CapabilityLookupDigest(token)
	if digest == "" {
		return nil, ErrNotFound
	}
	w, err := s.getByDigest(ctx, digest)
	switch {
	case err == nil:
		// Echo the presented cleartext back on the in-memory struct: the
		// dispatch handler keys its per-webhook rate-limit window and its
		// idempotency hash off it, and both must stay distinct per webhook.
		w.Token = token
		return w, nil
	case !errors.Is(err, ErrNotFound):
		return nil, err
	case s.unhashed.Load() <= 0:
		return nil, ErrNotFound
	}
	return s.getByUnhashedCleartext(ctx, token)
}

// getByDigest is the hot path: one equality against the unique partial index
// on token_hash.
func (s *WebhookStore) getByDigest(ctx context.Context, digest string) (*Webhook, error) {
	rows, err := s.db.QueryContext(ctx,
		webhookSelect+` WHERE token_hash = ? AND deleted_at IS NULL`, digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	w, err := scanWebhook(rows)
	if err != nil {
		return nil, err
	}
	// Constant-time confirmation that the row the index matched really is
	// the one this token digests to, so the decision never rests on a
	// short-circuiting byte comparison.
	if !CapabilityDigestEqual(w.TokenHash, digest) {
		return nil, ErrNotFound
	}
	return w, nil
}

// getByUnhashedCleartext resolves a token against rows the backfill could not
// hash. It runs only when the constructor reported such rows and only after
// the digest lookup missed.
//
// WHY A CLEARTEXT FALLBACK IS THE RIGHT ANSWER HERE. The alternative is to
// keep the lookup digest-only, which means an UPDATE that lost a race with
// SQLite's single writer silently 404s a webhook whose token is still valid,
// for every sender, until someone restarts the daemon — a hardening change
// taking down working integrations by accident. The fallback gives up nothing
// the hardening was protecting: it matches ONLY rows whose token_hash is still
// empty, and those rows are precisely the ones whose cleartext is sitting in
// the table anyway, so a reader of the database file learns nothing new from
// its existence. It cannot be used to replay a digest (CapabilityLookupDigest
// rejects a value carrying the scheme prefix) nor a redaction marker (same
// guard), and it disappears row by row as each match re-attempts the hashing.
func (s *WebhookStore) getByUnhashedCleartext(ctx context.Context, token string) (*Webhook, error) {
	rows, err := s.db.QueryContext(ctx,
		webhookSelect+` WHERE COALESCE(token_hash, '') = '' AND token = ? AND deleted_at IS NULL`, token)
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		rows.Close()
		return nil, ErrNotFound
	}
	w, err := scanWebhook(rows)
	// The cursor must be closed BEFORE the repair below: it holds a
	// connection, and a pool capped at one (every test here, and any caller
	// that pins SQLite to a single connection) would deadlock on the UPDATE
	// waiting for the connection this row is still reading.
	rows.Close()
	if err != nil {
		return nil, err
	}
	w.Token = token
	// Self-heal: retry the write the backfill lost. Best-effort — the fire
	// this lookup serves must not fail because the retry did.
	digest := HashCapabilityToken(token)
	if _, uerr := s.db.ExecContext(ctx,
		`UPDATE pipeline_webhooks SET token_hash = ?, token = ? WHERE id = ? AND COALESCE(token_hash, '') = ''`,
		digest, redactedWebhookToken(w.ID), w.ID,
	); uerr != nil {
		slog.Warn("capability tokens: re-hashing a cleartext row failed", "id", w.ID, "error", uerr)
		return w, nil
	}
	w.TokenHash = digest
	s.unhashed.Add(-1)
	slog.Info("capability tokens: hashed a row the backfill had missed", "id", w.ID)
	return w, nil
}

// List returns the workspace's webhooks ordered by created_at desc.
func (s *WebhookStore) List(ctx context.Context, workspaceID string) ([]*Webhook, error) {
	rows, err := s.db.QueryContext(ctx,
		webhookSelect+` WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SoftDelete marks a webhook deleted; the dispatch path treats
// deleted_at IS NOT NULL as a 404, so disabled webhooks stop firing
// without leaking their existence.
func (s *WebhookStore) SoftDelete(ctx context.Context, id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		`UPDATE pipeline_webhooks SET deleted_at = ?, updated_at = ?, enabled = 0 WHERE id = ? AND deleted_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordFire updates a webhook's last_fired_at + last_status +
// last_run_id + fire_count after a dispatch. Called by the handler
// after Run returns (success or failure).
// RecordFire's second return value is the FRESH consecutive_fire_failures
// streak for this webhook, read back atomically via RETURNING (#2/A4,
// F20). A FAILED status increments the streak; any other status
// (COMPLETED, CANCELLED, WAITING, DEDUPED) resets it to 0 — the same
// reset-on-success / increment-on-failure shape as
// pipeline_schedules.consecutive_failures (ScheduleStore.recordRun).
// Doing the increment and the read in one statement (rather than an
// ExecContext followed by a separate SELECT) means two fires of the same
// webhook racing each other each see the true post-increment count, not
// a stale pre-increment one — so at most one of them can observe the
// streak crossing the alert threshold. The status value is only ever one
// of the fixed literals FireWebhook passes (never caller-supplied SQL),
// so splicing it into the SET clause is safe; it is still passed as a
// bound parameter, never concatenated into the query text.
//
// An unknown webhookID matches zero rows: the UPDATE...RETURNING then
// returns sql.ErrNoRows, which is swallowed to (0, nil) to preserve the
// pre-existing "unknown id is silently a no-op" contract (see
// TestWebhookStore_RecordFire_UnknownID_NoError) — this can legitimately
// race a concurrent delete of the very webhook that just fired.
func (s *WebhookStore) RecordFire(ctx context.Context, webhookID, runID, status string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	streakExpr := "0"
	if status == "FAILED" {
		streakExpr = "consecutive_fire_failures + 1"
	}
	var streak int64
	err := s.db.QueryRowContext(ctx, `
UPDATE pipeline_webhooks
SET last_fired_at = ?, last_status = ?, last_run_id = ?, fire_count = fire_count + 1, updated_at = ?,
    consecutive_fire_failures = `+streakExpr+`
WHERE id = ?
RETURNING consecutive_fire_failures`,
		now, status, nullStr(runID), now, webhookID,
	).Scan(&streak)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return streak, nil
}

// ValidateSignature computes the HMAC-SHA256 of body using the
// webhook's signing_secret and compares against the supplied hex
// digest. Constant-time comparison so timing attacks can't
// fingerprint valid prefixes.
//
// Returns false if SigningSecret is empty: every webhook MUST have
// a secret to be dispatched. The previous behaviour "no-op pass on
// empty SigningSecret" let any legacy row (created before audit #490
// forced auto-generation, or persisted via a path that bypassed the
// HTTP CreateWebhook handler) accept unsigned POSTs to its public
// dispatch URL. Audit chain finding (A13.2 + A17.2): MEMBER creates
// webhook → public URL fires pipeline → no auth.
func (w *Webhook) ValidateSignature(body []byte, providedHex string) bool {
	if w.SigningSecret == "" {
		// No secret on this row = no signature is verifiable. Treat
		// the dispatch as unauthenticated rather than silently passing
		// it. The webhook needs to be re-created (or have its secret
		// rotated through whatever admin path lands) before it can
		// fire again.
		return false
	}
	// Senders are instructed (create handler + CLI output) to use the
	// GitHub-style "sha256=<hex>" header form; bare hex is accepted too
	// for backwards compatibility with pre-existing integrations.
	providedHex = strings.TrimPrefix(providedHex, "sha256=")
	if providedHex == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(w.SigningSecret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	// hmac.Equal does the constant-time work; we just have to feed
	// it byte slices of the same length.
	return hmac.Equal([]byte(expected), []byte(providedHex))
}

// ValidateTimestampedSignature verifies the HMAC-SHA256 of "<ts>.<body>"
// using the webhook's signing_secret, requiring ts (unix seconds) to be
// within tolerance of now. #1416 item 2: this is the pipeline-webhook
// mirror of the agent-webhook path's ts.body scheme
// (internal/webhook/handler.go:81-103,183-194).
//
// ValidateSignature's bare-body HMAC is replayable indefinitely — bounded
// only by the (up to 24h, and Forget-reopenable on a failed run)
// idempotency window, so anyone who captures one signed delivery could
// re-fire the routine any time inside that window. A sender that adopts
// X-Crewship-Timestamp gets a signature that is cryptographically bound to
// the timestamp (so it can't be stripped or swapped) and useless once ts
// falls outside tolerance, closing that gap for senders who opt in.
//
// tolerance<=0 uses DefaultWebhookTimestampTolerance. now is passed in
// (rather than reading time.Now internally) so callers/tests can pin a
// deterministic clock; production callers pass time.Now().
func (w *Webhook) ValidateTimestampedSignature(body []byte, ts, providedHex string, now time.Time, tolerance time.Duration) bool {
	if w.SigningSecret == "" {
		// Mirrors ValidateSignature: no secret on this row means no
		// signature is verifiable, timestamped or not.
		return false
	}
	secs, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if tolerance <= 0 {
		tolerance = DefaultWebhookTimestampTolerance
	}
	// Compare in seconds against non-overflowing bounds rather than via
	// time.Sub — mirrors internal/webhook.Handler.timestampFresh's
	// overflow-safety rationale: an absurd far-future/far-past ts could
	// otherwise wrap an int64-nanosecond Duration and defeat the check.
	tolSec := int64(tolerance / time.Second)
	nowSec := now.Unix()
	if secs < nowSec-tolSec || secs > nowSec+tolSec {
		return false
	}
	providedHex = strings.TrimPrefix(providedHex, "sha256=")
	if providedHex == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(w.SigningSecret))
	_, _ = mac.Write([]byte(ts + "."))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(providedHex))
}

// rateLimiter is the in-memory throttle for webhooks. Per-token
// sliding 60-second window with a single integer counter; cleared
// when the window rolls. The trade-off vs. a token-bucket: simpler,
// approximate, no goroutines. Good enough for the "Stripe storm"
// guardrail — it's not meant to be a precise rate limit.
type rateLimiter struct {
	mu      sync.Mutex
	windows map[string]*rateWindow
}

type rateWindow struct {
	startedAt time.Time
	count     int64
}

var globalRateLimiter = &rateLimiter{windows: map[string]*rateWindow{}}

// rateLimiterPruneThreshold bounds how large the window map is allowed to
// grow before allow() opportunistically sweeps long-expired entries.
// #1416 nit: the map was previously never pruned at all — a token that
// fires once and is never reused (e.g. an attacker who mints many one-off
// webhook tokens, or a legitimate integration that's since been deleted)
// left a permanent entry for the life of the process. Sized generously so
// the sweep stays rare in the common case of a handful of active tokens;
// bounded by distinct tokens (as the original comment already noted), so
// this is defense-in-depth rather than a fix for unbounded growth.
const rateLimiterPruneThreshold = 10000

// allow reports whether a hit on `key` is within the limit. limit=0
// is treated as unlimited.
//
// The increment must run UNDER the mutex (not via atomic.Add after
// unlock) — otherwise two goroutines that both lock, both see an
// expired window, both install a new window, and then both increment
// outside the lock can race in subtle ways when multiple keys
// interleave. Holding the mutex through increment + decision keeps
// the limit semantics simple and observable.
func (r *rateLimiter) allow(key string, limit int) bool {
	if limit <= 0 {
		return true
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.windows) > rateLimiterPruneThreshold {
		r.pruneLocked(now)
	}
	w, ok := r.windows[key]
	if !ok || now.Sub(w.startedAt) >= time.Minute {
		w = &rateWindow{startedAt: now}
		r.windows[key] = w
	}
	w.count++
	return w.count <= int64(limit)
}

// pruneLocked deletes every window whose minute has already elapsed. No
// goroutines/timers by design (matches the rest of this limiter's
// "simpler, approximate, no goroutines" trade-off) — pruning rides the
// existing allow() call path, amortized across whichever caller happens to
// trip the threshold. Caller must hold r.mu.
func (r *rateLimiter) pruneLocked(now time.Time) {
	for k, w := range r.windows {
		if now.Sub(w.startedAt) >= time.Minute {
			delete(r.windows, k)
		}
	}
}

// AllowFire is the public throttle entrypoint used by the webhook
// handler. Returns false when the token has exceeded its
// rate_limit_per_min in the current 60s window.
func AllowWebhookFire(token string, limit int) bool {
	return globalRateLimiter.allow(token, limit)
}

// webhookSelect deliberately does not read `token`. Post-#1888 that column
// holds a dead redaction marker; the digest in token_hash is the only value
// with meaning.
const webhookSelect = `
SELECT id, workspace_id, name, target_pipeline_id, target_pipeline_version,
       COALESCE(token_hash, ''), COALESCE(signing_secret, ''), inputs_template,
       enabled, rate_limit_per_min,
       last_fired_at, COALESCE(last_status, ''), COALESCE(last_run_id, ''),
       fire_count, created_at, updated_at, deleted_at
FROM pipeline_webhooks`

func scanWebhook(rs rowScanner) (*Webhook, error) {
	var (
		w             Webhook
		targetVersion sql.NullInt64
		lastFired     sql.NullString
		deletedAt     sql.NullString
		enabled       int
		createdAt     string
		updatedAt     string
	)
	err := rs.Scan(
		&w.ID, &w.WorkspaceID, &w.Name, &w.TargetPipelineID, &targetVersion,
		&w.TokenHash, &w.SigningSecret, &w.InputsTemplateJSON,
		&enabled, &w.RateLimitPerMin,
		&lastFired, &w.LastStatus, &w.LastRunID,
		&w.FireCount, &createdAt, &updatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
	// #1029: signing_secret is stored AES-256-GCM encrypted at rest; materialize
	// the plaintext for in-memory use (HMAC verify + the show-once create
	// response) so encryption is transparent to every consumer. A bare
	// (legacy/key-less) value passes through unchanged; on a decrypt error the
	// raw value is kept, which just makes HMAC verification fail safely.
	w.SigningSecret, _ = encryption.DecryptIfEncrypted(w.SigningSecret)
	w.Enabled = enabled != 0
	if targetVersion.Valid {
		v := int(targetVersion.Int64)
		w.TargetPipelineVersion = &v
	}
	w.LastFiredAt = parseTimePtr(lastFired.String)
	w.CreatedAt = parseTimeOrZero(createdAt)
	w.UpdatedAt = parseTimeOrZero(updatedAt)
	if deletedAt.Valid {
		t := parseTimeOrZero(deletedAt.String)
		w.DeletedAt = &t
	}
	return &w, nil
}

func generateWebhookID() string {
	ts := time.Now().UnixMilli()
	c := webhookIDCounter.Add(1)
	tail := c % 65536
	const hexdigits = "0123456789abcdef"
	rb := make([]byte, 4)
	if _, err := rand.Read(rb); err != nil {
		for i := range rb {
			rb[i] = byte(c >> (i * 8))
		}
	}
	return "pwh_c" + strconv.FormatInt(ts, 36) +
		string([]byte{
			hexdigits[(tail>>12)&0xf], hexdigits[(tail>>8)&0xf],
			hexdigits[(tail>>4)&0xf], hexdigits[tail&0xf],
		}) + hex.EncodeToString(rb)
}

var webhookIDCounter atomic.Uint64

// generateWebhookToken returns a 32-byte hex-encoded random token
// (64 hex chars on the wire). 256 bits of entropy makes brute-force
// guessing infeasible — knowing the token is the auth surface.
func generateWebhookToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "wh_" + hex.EncodeToString(b), nil
}
