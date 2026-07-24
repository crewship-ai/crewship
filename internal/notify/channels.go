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
	// ChannelShoutrrr delivers via github.com/nicholas-fedor/shoutrrr — an
	// Apprise-style service URL (slack://, discord://, telegram://, …).
	// Which concrete service is added alongside webhook/email (#1412); it
	// does not replace either. See Provider for the service selector.
	ChannelShoutrrr ChannelType = "shoutrrr"
)

// Provider names for ChannelShoutrrr channels. These are the MVP-supported
// shoutrrr services (issue #1412) — the library supports many more, but the
// providers-registry endpoint and channel-create validation only admit
// this set for now, matching the URL schemes exposed in the CLI/UI.
const (
	ProviderSlack    = "slack"
	ProviderDiscord  = "discord"
	ProviderTelegram = "telegram"
)

// shoutrrrSchemes maps a Provider name to the URL scheme shoutrrr expects.
// Kept as a lookup (rather than provider == scheme) so a provider name can
// diverge from its wire scheme without touching call sites.
var shoutrrrSchemes = map[string]string{
	ProviderSlack:    "slack",
	ProviderDiscord:  "discord",
	ProviderTelegram: "telegram",
}

// SupportedProviders lists the MVP shoutrrr provider names, in a stable
// order, for the providers-registry API/CLI surface.
func SupportedProviders() []string {
	return []string{ProviderSlack, ProviderDiscord, ProviderTelegram}
}

// ChannelScope distinguishes a workspace-wide channel (managed by an
// ADMIN/OWNER, available to every category/user the admin allowlists) from
// a personal channel a single member owns (their own Telegram/webhook).
type ChannelScope string

const (
	ScopeWorkspace ChannelScope = "workspace"
	ScopeUser      ChannelScope = "user"
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
	Events      []string    `json:"events"`        // subscribed run-terminal event types (legacy #850 path)
	Enabled     bool        `json:"enabled"`
	CreatedBy   string      `json:"created_by,omitempty"`
	CreatedAt   string      `json:"created_at,omitempty"`

	// Provider names the shoutrrr service (slack|discord|telegram) for
	// Type == ChannelShoutrrr. Empty for email/webhook.
	Provider string `json:"provider,omitempty"`
	// Scope is workspace (admin-managed, shared) or user (a member's own
	// personal channel). See ChannelScope.
	Scope ChannelScope `json:"scope"`
	// OwnerUserID is set for Scope == ScopeUser: the member who owns this
	// personal channel. Empty for workspace-scoped channels.
	OwnerUserID string `json:"owner_user_id,omitempty"`
	// Categories is the admin-set allowlist of #1412 notification
	// categories this channel may fan out to (approvals, security, …).
	// Empty means "every category" — the pre-#1412 default so existing
	// channels keep working unchanged.
	Categories []string `json:"categories,omitempty"`
	// MinPriority is the inbox-item priority floor below which this
	// channel is skipped by the preference router (low|medium|high|urgent).
	MinPriority string `json:"min_priority,omitempty"`

	// Secret is the webhook HMAC signing secret, OR the shoutrrr service
	// URL (decrypted) for Type == ChannelShoutrrr. Populated only on
	// dispatch-path reads and never serialized to API clients.
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
	Events      []string // subscribed event types; defaults to [run.failed] (legacy #850 path)
	CreatedBy   string

	// Provider is required for Type == ChannelShoutrrr (slack|discord|telegram).
	Provider string
	// ShoutrrrURL is the full Apprise-style service URL for
	// Type == ChannelShoutrrr (e.g. "slack://hook:TOKEN@webhook"). Stored
	// via the same secret_enc encrypt-at-rest path as the webhook secret.
	ShoutrrrURL string

	// Scope defaults to ScopeWorkspace when empty. ScopeUser requires
	// OwnerUserID.
	Scope ChannelScope
	// OwnerUserID is required when Scope == ScopeUser; the caller (handler)
	// is responsible for setting it to the AUTHENTICATED user, never a
	// client-supplied id, so a member can't create a personal channel on
	// another member's behalf.
	OwnerUserID string
	// Categories is the admin category allowlist. Empty = every category.
	Categories []string
	// MinPriority defaults to "low" (no floor) when empty.
	MinPriority string
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
	scope := in.Scope
	if scope == "" {
		scope = ScopeWorkspace
	}
	if scope != ScopeWorkspace && scope != ScopeUser {
		return Channel{}, fmt.Errorf("notify: unknown scope %q (want workspace or user)", scope)
	}
	if scope == ScopeUser && in.OwnerUserID == "" {
		return Channel{}, fmt.Errorf("notify: a user-scoped (personal) channel needs an owner")
	}
	if scope == ScopeWorkspace && in.OwnerUserID != "" {
		return Channel{}, fmt.Errorf("notify: a workspace-scoped channel cannot have a personal owner")
	}
	categories, err := normalizeCategories(in.Categories)
	if err != nil {
		return Channel{}, err
	}
	minPriority := strings.ToLower(strings.TrimSpace(in.MinPriority))
	if minPriority == "" {
		minPriority = "low"
	}
	switch minPriority {
	case "low", "medium", "high", "urgent":
	default:
		return Channel{}, fmt.Errorf("notify: unknown min_priority %q (want low, medium, high, or urgent)", minPriority)
	}

	ch := Channel{
		ID:          generateChannelID(),
		WorkspaceID: in.WorkspaceID,
		Type:        in.Type,
		Events:      events,
		Enabled:     true,
		CreatedBy:   in.CreatedBy,
		Scope:       scope,
		OwnerUserID: in.OwnerUserID,
		Categories:  categories,
		MinPriority: minPriority,
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
	case ChannelShoutrrr:
		provider := strings.ToLower(strings.TrimSpace(in.Provider))
		scheme, ok := shoutrrrSchemes[provider]
		if !ok {
			return Channel{}, fmt.Errorf("notify: unknown provider %q (want one of %v)", provider, SupportedProviders())
		}
		rawURL := strings.TrimSpace(in.ShoutrrrURL)
		if err := validateShoutrrrURL(rawURL, scheme); err != nil {
			return Channel{}, err
		}
		ch.Provider = provider
		enc, err := encryption.Encrypt(rawURL)
		if err != nil {
			return Channel{}, fmt.Errorf("notify: encrypt shoutrrr url: %w", err)
		}
		secretEnc = enc
		ch.Secret = rawURL // returned once, to the creator (contains the token)
	default:
		return Channel{}, fmt.Errorf("notify: unknown channel type %q (want email, webhook, or shoutrrr)", in.Type)
	}

	cfg, err := json.Marshal(channelConfig{URL: ch.URL, To: ch.To})
	if err != nil {
		return Channel{}, fmt.Errorf("notify: marshal config: %w", err)
	}
	eventsJSON, err := json.Marshal(ch.Events)
	if err != nil {
		return Channel{}, fmt.Errorf("notify: marshal events: %w", err)
	}
	categoriesJSON, err := json.Marshal(ch.Categories)
	if err != nil {
		return Channel{}, fmt.Errorf("notify: marshal categories: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO notification_channels
    (id, workspace_id, type, provider, config_json, secret_enc, events_json,
     enabled, created_by, scope, owner_user_id, categories_json, min_priority)
VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
		ch.ID, ch.WorkspaceID, string(ch.Type), ch.Provider, string(cfg), secretEnc, string(eventsJSON),
		nullStr(ch.CreatedBy), string(ch.Scope), nullStr(ch.OwnerUserID), string(categoriesJSON), ch.MinPriority); err != nil {
		return Channel{}, fmt.Errorf("notify: insert channel: %w", err)
	}
	return ch, nil
}

// normalizeCategories validates the admin category allowlist. An empty
// request means "every category" (the pre-#1412 default). The mute-all
// sentinel is never a legal entry here — it's a preference-matrix cell
// state, not something a channel is scoped to.
func normalizeCategories(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range in {
		c := strings.ToLower(strings.TrimSpace(raw))
		if !ValidCategory(c) {
			return nil, fmt.Errorf("notify: unknown category %q (want one of %v)", raw, AllCategories)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
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

// List returns the channels VISIBLE to viewerUserID: every workspace-
// scoped channel, plus viewerUserID's OWN personal (scope=user) channels —
// never another member's personal channel. Secrets are redacted. This is
// the API-facing read (GET /api/v1/notification-channels); without the
// viewer filter a personal channel's existence (its URL/provider — not
// its secret, but still private metadata like a member's own phone
// number via a Telegram chat id) would leak to every workspace member.
func (s *ChannelStore) List(ctx context.Context, workspaceID, viewerUserID string) ([]Channel, error) {
	all, err := s.query(ctx, workspaceID, "", false)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if c.Scope == ScopeUser && c.OwnerUserID != viewerUserID {
			continue
		}
		out = append(out, c)
	}
	return out, nil
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

// Get resolves one channel (secret redacted) — used by the API layer to
// check scope/ownership before authorizing a mutation.
func (s *ChannelStore) Get(ctx context.Context, workspaceID, id string) (Channel, error) {
	rows, err := s.query(ctx, workspaceID, id, false)
	if err != nil {
		return Channel{}, err
	}
	if len(rows) == 0 {
		return Channel{}, ErrNotFound
	}
	return rows[0], nil
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

// PatchInput is the partial-update payload for PATCH
// /api/v1/notification-channels/{id}. Nil fields are left unchanged.
type PatchInput struct {
	Enabled     *bool
	Categories  *[]string // nil = unchanged; non-nil empty slice = "every category"
	MinPriority *string
	Events      *[]string
}

// Patch applies a partial update, scoped to the workspace. Returns false
// when nothing matched (wrong id/workspace, or soft-deleted).
func (s *ChannelStore) Patch(ctx context.Context, workspaceID, id string, in PatchInput) (bool, error) {
	sets := []string{"updated_at = datetime('now','subsec')"}
	args := []any{}
	if in.Enabled != nil {
		sets = append(sets, "enabled = ?")
		v := 0
		if *in.Enabled {
			v = 1
		}
		args = append(args, v)
	}
	if in.Categories != nil {
		cats, err := normalizeCategories(*in.Categories)
		if err != nil {
			return false, err
		}
		b, err := json.Marshal(cats)
		if err != nil {
			return false, fmt.Errorf("notify: marshal categories: %w", err)
		}
		sets = append(sets, "categories_json = ?")
		args = append(args, string(b))
	}
	if in.MinPriority != nil {
		mp := strings.ToLower(strings.TrimSpace(*in.MinPriority))
		switch mp {
		case "low", "medium", "high", "urgent":
		default:
			return false, fmt.Errorf("notify: unknown min_priority %q (want low, medium, high, or urgent)", mp)
		}
		sets = append(sets, "min_priority = ?")
		args = append(args, mp)
	}
	if in.Events != nil {
		events, err := normalizeEvents(*in.Events)
		if err != nil {
			return false, err
		}
		b, err := json.Marshal(events)
		if err != nil {
			return false, fmt.Errorf("notify: marshal events: %w", err)
		}
		sets = append(sets, "events_json = ?")
		args = append(args, string(b))
	}
	if len(sets) == 1 {
		return false, fmt.Errorf("notify: patch has no fields to update")
	}
	args = append(args, id, workspaceID)
	q := "UPDATE notification_channels SET " + strings.Join(sets, ", ") +
		" WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL"
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, fmt.Errorf("notify: patch channel: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListForUser returns the channels available to userID for the
// preference-matrix / delivery-routing path: every enabled workspace-scope
// channel (subject to the caller checking its Categories allowlist) plus
// userID's own enabled personal (scope=user) channels. Secrets are
// decrypted — this is a dispatch-adjacent read, not the API-facing List.
func (s *ChannelStore) ListForUser(ctx context.Context, workspaceID, userID string) ([]Channel, error) {
	all, err := s.query(ctx, workspaceID, "", true)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if !c.Enabled {
			continue
		}
		switch c.Scope {
		case ScopeUser:
			if c.OwnerUserID == userID {
				out = append(out, c)
			}
		default: // ScopeWorkspace (and legacy rows with scope="" pre-#1412, treated as workspace)
			out = append(out, c)
		}
	}
	return out, nil
}

// AllowsCategory reports whether the channel's admin allowlist admits
// category. An empty allowlist (the pre-#1412 default) admits everything.
func (c Channel) AllowsCategory(category string) bool {
	if len(c.Categories) == 0 {
		return true
	}
	for _, want := range c.Categories {
		if want == category {
			return true
		}
	}
	return false
}

// query is the shared read path. When id != "" it filters to that row;
// when withSecret it decrypts secret_enc into Channel.Secret.
func (s *ChannelStore) query(ctx context.Context, workspaceID, id string, withSecret bool) ([]Channel, error) {
	q := `
SELECT id, workspace_id, type, provider, config_json, secret_enc, events_json, enabled, created_by, created_at,
       scope, owner_user_id, categories_json, min_priority
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
			c              Channel
			typ, provider  string
			cfg            string
			secretEnc      sql.NullString
			eventsJSON     sql.NullString
			createdBy      sql.NullString
			enabled        int
			scope          string
			ownerUserID    sql.NullString
			categoriesJSON sql.NullString
			minPriority    string
		)
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &typ, &provider, &cfg, &secretEnc, &eventsJSON, &enabled, &createdBy, &c.CreatedAt,
			&scope, &ownerUserID, &categoriesJSON, &minPriority); err != nil {
			return nil, fmt.Errorf("notify: scan channel: %w", err)
		}
		c.Type = ChannelType(typ)
		c.Provider = provider
		c.Enabled = enabled != 0
		c.CreatedBy = createdBy.String
		c.Scope = ChannelScope(scope)
		if c.Scope == "" {
			c.Scope = ScopeWorkspace
		}
		c.OwnerUserID = ownerUserID.String
		c.MinPriority = minPriority
		var parsed channelConfig
		_ = json.Unmarshal([]byte(cfg), &parsed)
		c.URL = parsed.URL
		c.To = parsed.To
		if eventsJSON.Valid && eventsJSON.String != "" {
			_ = json.Unmarshal([]byte(eventsJSON.String), &c.Events)
		}
		if categoriesJSON.Valid && categoriesJSON.String != "" {
			_ = json.Unmarshal([]byte(categoriesJSON.String), &c.Categories)
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

// validateShoutrrrURL rejects anything that isn't a plausible Apprise-style
// service URL for the given scheme (e.g. "slack://...", "discord://...",
// "telegram://..."). Deliberately conservative like validateWebhookURL:
// scheme + non-empty host/opaque part, not a full shoutrrr-side parse (that
// happens at send time, and a malformed URL then fails the test-send with a
// clear error rather than silently rejecting a shape shoutrrr itself would
// accept).
func validateShoutrrrURL(raw, wantScheme string) error {
	if raw == "" {
		return fmt.Errorf("notify: shoutrrr channel needs a url")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("notify: invalid shoutrrr url: %w", err)
	}
	if u.Scheme != wantScheme {
		return fmt.Errorf("notify: shoutrrr url scheme must be %q for this provider, got %q", wantScheme, u.Scheme)
	}
	if u.Host == "" && u.Opaque == "" {
		return fmt.Errorf("notify: shoutrrr url missing host")
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
