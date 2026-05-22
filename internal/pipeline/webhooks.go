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
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

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
	Token                 string
	SigningSecret         string // empty = no HMAC verification
	InputsTemplateJSON    string
	Enabled               bool
	RateLimitPerMin       int
	LastFiredAt           *time.Time
	LastStatus            string
	LastRunID             string
	FireCount             int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
	DeletedAt             *time.Time
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
}

// NewWebhookStore returns a store backed by a v82+ DB.
func NewWebhookStore(db *sql.DB) *WebhookStore {
	return &WebhookStore{db: db}
}

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

	if in.ID == "" {
		id := generateWebhookID()
		token, err := generateWebhookToken()
		if err != nil {
			return nil, fmt.Errorf("mint webhook token: %w", err)
		}
		_, err = s.db.ExecContext(ctx, `
INSERT INTO pipeline_webhooks (
    id, workspace_id, name, target_pipeline_id, target_pipeline_version,
    token, signing_secret, inputs_template,
    enabled, rate_limit_per_min,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.WorkspaceID, in.Name, in.TargetPipelineID,
			nullInt(in.TargetPipelineVersion),
			token, nullStr(in.SigningSecret), string(tmplJSON),
			boolToInt(in.Enabled), in.RateLimitPerMin,
			now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("insert webhook: %w", err)
		}
		return s.GetByID(ctx, id)
	}

	_, err = s.db.ExecContext(ctx, `
UPDATE pipeline_webhooks
SET name = ?, target_pipeline_id = ?, target_pipeline_version = ?,
    signing_secret = ?, inputs_template = ?, enabled = ?, rate_limit_per_min = ?,
    updated_at = ?
WHERE id = ? AND deleted_at IS NULL`,
		in.Name, in.TargetPipelineID, nullInt(in.TargetPipelineVersion),
		nullStr(in.SigningSecret), string(tmplJSON),
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
	if token == "" {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, webhookSelect+` WHERE token = ? AND deleted_at IS NULL`, token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, ErrNotFound
	}
	return scanWebhook(rows)
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
func (s *WebhookStore) RecordFire(ctx context.Context, webhookID, runID, status string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `
UPDATE pipeline_webhooks
SET last_fired_at = ?, last_status = ?, last_run_id = ?, fire_count = fire_count + 1, updated_at = ?
WHERE id = ?`,
		now, status, nullStr(runID), now, webhookID,
	)
	return err
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
	w, ok := r.windows[key]
	if !ok || now.Sub(w.startedAt) >= time.Minute {
		w = &rateWindow{startedAt: now}
		r.windows[key] = w
	}
	w.count++
	return w.count <= int64(limit)
}

// AllowFire is the public throttle entrypoint used by the webhook
// handler. Returns false when the token has exceeded its
// rate_limit_per_min in the current 60s window.
func AllowWebhookFire(token string, limit int) bool {
	return globalRateLimiter.allow(token, limit)
}

const webhookSelect = `
SELECT id, workspace_id, name, target_pipeline_id, target_pipeline_version,
       token, COALESCE(signing_secret, ''), inputs_template,
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
		&w.Token, &w.SigningSecret, &w.InputsTemplateJSON,
		&enabled, &w.RateLimitPerMin,
		&lastFired, &w.LastStatus, &w.LastRunID,
		&w.FireCount, &createdAt, &updatedAt, &deletedAt,
	)
	if err != nil {
		return nil, err
	}
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
