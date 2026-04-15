package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/webhook"
)

// WebhookConfig drives outbound backup event POSTs. Empty URL disables
// delivery entirely — the runner detects this and skips the work so
// unconfigured deployments pay zero overhead.
//
// The signature scheme matches the inbound path in internal/webhook:
// HMAC-SHA256 over the request body, hex-encoded, in the
// X-Crewship-Signature header as "sha256=<hex>". Consumers verify via
// webhook.ValidateHMAC after stripping the "sha256=" prefix.
type WebhookConfig struct {
	URL     string
	Secret  string
	Timeout time.Duration // 0 → 10 seconds
}

// WebhookConfigFromEnv reads the process environment for outbound
// backup webhook settings. Returns the zero value (empty URL) when
// neither env var is set, so production deployments without the
// feature pay no cost and need no guard in the caller.
func WebhookConfigFromEnv() WebhookConfig {
	return WebhookConfig{
		URL:    os.Getenv("CREWSHIP_BACKUP_WEBHOOK_URL"),
		Secret: os.Getenv("CREWSHIP_BACKUP_WEBHOOK_SECRET"),
	}
}

// WebhookEvent is the JSON payload POSTed to the admin-configured
// webhook URL. Field names mirror Prometheus metric labels so
// downstream consumers can key off a single vocabulary.
type WebhookEvent struct {
	Event       string    `json:"event"` // backup.created | backup.failed | backup.restored
	Timestamp   time.Time `json:"timestamp"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	Scope       string    `json:"scope"`
	Path        string    `json:"path,omitempty"`
	Bytes       int64     `json:"bytes,omitempty"`
	SHA256      string    `json:"payload_sha256,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// SendEvent POSTs the event to cfg.URL with an HMAC-SHA256 signature.
// Empty URL is a no-op (returns nil). Errors are returned for the
// caller to log — the runner invokes SendEvent in a goroutine so a
// misbehaving webhook cannot block the backup run itself.
//
// The signing secret is required whenever URL is set; sending a body
// unsigned would let any network listener forge events to a downstream
// consumer that trusts the feed.
func SendEvent(ctx context.Context, cfg WebhookConfig, event WebhookEvent) error {
	if cfg.URL == "" {
		return nil
	}
	if cfg.Secret == "" {
		return fmt.Errorf("backup webhook: URL configured without secret")
	}
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("backup webhook: marshal event: %w", err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("backup webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Crewship-Event", event.Event)
	req.Header.Set("X-Crewship-Signature", "sha256="+webhook.ComputeHMAC(body, cfg.Secret))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("backup webhook: POST %s: %w", cfg.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("backup webhook: %s returned %d", cfg.URL, resp.StatusCode)
	}
	return nil
}

// SendEventAsync fires SendEvent in a detached goroutine; errors are
// written to the supplied logger (if non-nil) but do not surface to
// the caller — this is the wiring the runner uses so webhook outages
// never block a backup from completing.
func SendEventAsync(cfg WebhookConfig, event WebhookEvent, logger func(string)) {
	if cfg.URL == "" {
		return
	}
	go func() {
		if err := SendEvent(context.Background(), cfg, event); err != nil && logger != nil {
			logger(fmt.Sprintf("backup webhook: %v", err))
		}
	}()
}
