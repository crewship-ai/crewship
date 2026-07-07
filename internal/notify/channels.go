// Package notify delivers outbound notifications — e-mail and signed
// webhooks — when a routine run reaches a terminal state (issue #850).
// The in-product inbox (notify step, completion ping) is the other half;
// this package reaches people who aren't looking at Crewship.
//
// A ChannelStore persists workspace-scoped delivery targets; a Dispatcher
// fans a NotificationEvent out to every enabled channel, best-effort with
// retries, so a flaky receiver never fails the run that triggered it.
package notify

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// ChannelType is the delivery mechanism for a channel.
type ChannelType string

const (
	// ChannelEmail sends an e-mail via the instance mailer.
	ChannelEmail ChannelType = "email"
	// ChannelWebhook POSTs a signed JSON payload to a URL.
	ChannelWebhook ChannelType = "webhook"
)

// ErrNotFound is returned when a channel id doesn't resolve within the
// workspace (or was soft-deleted).
var ErrNotFound = errors.New("notify: channel not found")

// Channel is a resolved delivery target. Secret is only populated by the
// dispatch-path reads (ListEnabled / GetForDispatch); the API-facing List
// leaves it empty so a signing secret never leaves the server.
type Channel struct {
	ID          string      `json:"id"`
	WorkspaceID string      `json:"workspace_id"`
	Type        ChannelType `json:"type"`
	URL         string      `json:"url,omitempty"` // webhook
	To          string      `json:"to,omitempty"`  // email
	Events      []string    `json:"events"`        // subscribed run-terminal event types
	Enabled     bool        `json:"enabled"`
	CreatedBy   string      `json:"created_by,omitempty"`
	CreatedAt   string      `json:"created_at,omitempty"`

	// Secret is the webhook HMAC signing secret (decrypted). Populated
	// only on dispatch-path reads and never serialized to API clients.
	Secret string `json:"-"`
}

// Wants reports whether this channel is subscribed to the given event
// type. An empty subscription (legacy/none) is treated as failures-only.
func (c Channel) Wants(eventType string) bool {
	if len(c.Events) == 0 {
		return eventType == EventRunFailed
	}
	for _, e := range c.Events {
		if e == eventType {
			return true
		}
	}
	return false
}

// ChannelInput is the create payload.
type ChannelInput struct {
	WorkspaceID string
	Type        ChannelType
	URL         string   // webhook
	To          string   // email
	Secret      string   // webhook; auto-generated when empty
	Events      []string // subscribed event types; defaults to [run.failed]
	CreatedBy   string
}

// ChannelStore is the persistence layer for notification_channels (v133).
type ChannelStore struct {
	db *sql.DB
}

// NewChannelStore builds a store over the given DB handle.
func NewChannelStore(db *sql.DB) *ChannelStore { return &ChannelStore{db: db} }

// channelConfig is the JSON shape stored in config_json.
type channelConfig struct {
	URL string `json:"url,omitempty"`
	To  string `json:"to,omitempty"`
}

// Create validates and inserts a channel. For webhooks it encrypts the
// signing secret at rest and returns the plaintext secret ON THIS CALL
// ONLY (via Channel.Secret) so the caller can configure the receiver —
// it is never readable again. A blank webhook secret is auto-generated.
func (s *ChannelStore) Create(ctx context.Context, in ChannelInput) (Channel, error) {
	events, err := normalizeEvents(in.Events)
	if err != nil {
		return Channel{}, err
	}
	ch := Channel{
		ID:          generateChannelID(),
		WorkspaceID: in.WorkspaceID,
		Type:        in.Type,
		Events:      events,
		Enabled:     true,
		CreatedBy:   in.CreatedBy,
	}

	var secretEnc any // NULL for e-mail
	switch in.Type {
	case ChannelWebhook:
		if err := validateWebhookURL(in.URL); err != nil {
			return Channel{}, err
		}
		ch.URL = in.URL
		secret := strings.TrimSpace(in.Secret)
		if secret == "" {
			gen, err := generateSecret()
			if err != nil {
				return Channel{}, fmt.Errorf("notify: generate secret: %w", err)
			}
			secret = gen
		}
		enc, err := encryption.Encrypt(secret)
		if err != nil {
			return Channel{}, fmt.Errorf("notify: encrypt secret: %w", err)
		}
		secretEnc = enc
		ch.Secret = secret // returned once, to the creator
	case ChannelEmail:
		to := strings.TrimSpace(in.To)
		if to == "" || !strings.Contains(to, "@") {
			return Channel{}, fmt.Errorf("notify: email channel needs a valid to-address")
		}
		ch.To = to
	default:
		return Channel{}, fmt.Errorf("notify: unknown channel type %q (want email or webhook)", in.Type)
	}

	cfg, err := json.Marshal(channelConfig{URL: ch.URL, To: ch.To})
	if err != nil {
		return Channel{}, fmt.Errorf("notify: marshal config: %w", err)
	}
	eventsJSON, err := json.Marshal(ch.Events)
	if err != nil {
		return Channel{}, fmt.Errorf("notify: marshal events: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO notification_channels (id, workspace_id, type, config_json, secret_enc, events_json, enabled, created_by)
VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
		ch.ID, ch.WorkspaceID, string(ch.Type), string(cfg), secretEnc, string(eventsJSON), nullStr(ch.CreatedBy)); err != nil {
		return Channel{}, fmt.Errorf("notify: insert channel: %w", err)
	}
	return ch, nil
}

// normalizeEvents validates + de-dupes the requested event types,
// accepting a few friendly aliases ("completed"/"failed"/"all"). An
// empty request defaults to failures-only so hourly routines don't flood
// inboxes with success pings.
func normalizeEvents(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{EventRunFailed}, nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(e string) {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	for _, raw := range in {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "all", "*":
			add(EventRunCompleted)
			add(EventRunFailed)
		case "completed", "complete", "success", EventRunCompleted:
			add(EventRunCompleted)
		case "failed", "failure", "fail", EventRunFailed:
			add(EventRunFailed)
		default:
			return nil, fmt.Errorf("notify: unknown event %q (want completed, failed, or all)", raw)
		}
	}
	return out, nil
}

// List returns a workspace's live channels with secrets redacted — this
// is the API-facing read.
func (s *ChannelStore) List(ctx context.Context, workspaceID string) ([]Channel, error) {
	return s.query(ctx, workspaceID, "", false)
}

// ListEnabled returns the enabled channels with decrypted secrets, for
// the dispatch path.
func (s *ChannelStore) ListEnabled(ctx context.Context, workspaceID string) ([]Channel, error) {
	all, err := s.query(ctx, workspaceID, "", true)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}

// GetForDispatch resolves one channel (decrypted secret) for a test send.
func (s *ChannelStore) GetForDispatch(ctx context.Context, workspaceID, id string) (Channel, error) {
	rows, err := s.query(ctx, workspaceID, id, true)
	if err != nil {
		return Channel{}, err
	}
	if len(rows) == 0 {
		return Channel{}, ErrNotFound
	}
	return rows[0], nil
}

// Delete soft-deletes a channel scoped to the workspace. Returns false
// when nothing matched.
func (s *ChannelStore) Delete(ctx context.Context, workspaceID, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE notification_channels SET deleted_at = datetime('now','subsec'), updated_at = datetime('now','subsec')
WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, id, workspaceID)
	if err != nil {
		return false, fmt.Errorf("notify: delete channel: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// query is the shared read path. When id != "" it filters to that row;
// when withSecret it decrypts secret_enc into Channel.Secret.
func (s *ChannelStore) query(ctx context.Context, workspaceID, id string, withSecret bool) ([]Channel, error) {
	q := `
SELECT id, workspace_id, type, config_json, secret_enc, events_json, enabled, created_by, created_at
FROM notification_channels
WHERE workspace_id = ? AND deleted_at IS NULL`
	args := []any{workspaceID}
	if id != "" {
		q += ` AND id = ?`
		args = append(args, id)
	}
	q += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("notify: query channels: %w", err)
	}
	defer rows.Close()

	var out []Channel
	for rows.Next() {
		var (
			c          Channel
			typ, cfg   string
			secretEnc  sql.NullString
			eventsJSON sql.NullString
			createdBy  sql.NullString
			enabled    int
		)
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &typ, &cfg, &secretEnc, &eventsJSON, &enabled, &createdBy, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("notify: scan channel: %w", err)
		}
		c.Type = ChannelType(typ)
		c.Enabled = enabled != 0
		c.CreatedBy = createdBy.String
		var parsed channelConfig
		_ = json.Unmarshal([]byte(cfg), &parsed)
		c.URL = parsed.URL
		c.To = parsed.To
		if eventsJSON.Valid && eventsJSON.String != "" {
			_ = json.Unmarshal([]byte(eventsJSON.String), &c.Events)
		}
		if withSecret && secretEnc.Valid && secretEnc.String != "" {
			dec, err := encryption.Decrypt(secretEnc.String)
			if err != nil {
				return nil, fmt.Errorf("notify: decrypt secret for %s: %w", c.ID, err)
			}
			c.Secret = dec
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// validateWebhookURL rejects anything that isn't a plausible outbound
// http(s) endpoint. It is deliberately conservative but does NOT do full
// SSRF blocking (private-IP denylist) — webhook channels are configured
// by workspace managers, the same trust model as agent hooks. Hardening
// to a shared SSRF guard is a follow-up.
func validateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("notify: webhook channel needs a url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("notify: invalid webhook url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("notify: webhook url must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("notify: webhook url missing host")
	}
	return nil
}

func generateChannelID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "nch_" + hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return "nch_" + hex.EncodeToString(b)
}

// generateSecret returns 32 bytes of hex entropy for webhook signing.
func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
