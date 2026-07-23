package notify

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/egresspolicy"
	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/scrubber"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// webhookTransport is the http.RoundTripper used for all outbound webhook
// deliveries. It defaults to an SSRF-safe transport whose DialContext
// re-resolves the host at connect time and refuses any private / loopback /
// link-local IP — the runtime defence against a channel URL that RESOLVES to
// RFC1918 / 169.254.169.254 (cloud metadata IMDS) even if it looked public at
// channel-create time (DNS-rebind / split-horizon SSRF). Before #1367 the
// dispatcher used a bare http.Client with NO SSRF guard at all.
//
// It is a package var (not a const) so the test binary can swap in a
// loopback-allowing transport — httptest servers bind 127.0.0.1, which
// SafeTransport blocks. Production code never reassigns it; see TestMain in
// this package for the test override.
var webhookTransport http.RoundTripper = httpsafe.SafeTransport()

// SetWebhookTransportForTesting swaps the webhook transport — normally the
// SSRF-safe SafeTransport, which blocks loopback — so tests in OTHER packages
// (e.g. the internal/api notify-channel handler test) can deliver to an
// httptest server on 127.0.0.1. Returns a restore func. Production never calls
// it; this package's own tests use the TestMain override instead.
func SetWebhookTransportForTesting(rt http.RoundTripper) func() {
	prev := webhookTransport
	webhookTransport = rt
	return func() { webhookTransport = prev }
}

// Event types. These mirror the run terminal states the dispatcher fires
// on and become the "event" field of the webhook payload.
const (
	EventRunCompleted = "run.completed"
	EventRunFailed    = "run.failed"
)

// outputPreviewCap bounds the (scrubbed) output snippet carried in a
// notification so a large run deliverable can't bloat an e-mail or
// webhook body.
const outputPreviewCap = 1024

// NotificationEvent is the terminal-run fact a Dispatcher fans out.
type NotificationEvent struct {
	Type        string // EventRunCompleted | EventRunFailed
	WorkspaceID string
	RunID       string
	RoutineSlug string
	Status      string
	// AuthorCrewID is the crew that authored the routine whose run fired
	// this event. It keys the crew egress allowlist (#1367): a webhook
	// delivery is gated on the SAME crews.network_mode/allowed_domains dial
	// the sidecar proxy enforces for that crew's agents, so a restricted
	// crew cannot exfiltrate through a notify channel a non-allowlisted host.
	// Empty = no crew scope (e.g. a manual `notifychannel test` send) → the
	// gate allows, the SSRF guard still applies.
	AuthorCrewID  string
	OutputPreview string // raw; scrubbed + capped by the dispatcher
	TriggeredBy   string
}

// webhookPayload is the JSON POSTed to a webhook channel and the bytes
// the HMAC signs.
type webhookPayload struct {
	Event         string `json:"event"`
	RunID         string `json:"run_id"`
	Routine       string `json:"routine"`
	Status        string `json:"status"`
	OutputPreview string `json:"output_preview,omitempty"`
	TriggeredBy   string `json:"triggered_by,omitempty"`
}

// ChannelLister is the slice of ChannelStore the dispatcher needs. An
// interface keeps the dispatcher testable without a DB.
type ChannelLister interface {
	ListEnabled(ctx context.Context, workspaceID string) ([]Channel, error)
}

// Dispatcher delivers NotificationEvents to a workspace's enabled
// channels. Delivery is best-effort: a failing channel is logged and
// retried, but never surfaces an error to the run that triggered it.
type Dispatcher struct {
	lister      ChannelLister
	mail        mailer.Mailer
	scrub       *scrubber.Scrubber
	db          *sql.DB // crew egress-policy source (#1367); nil = no crew gate
	logger      *slog.Logger
	maxAttempts int
	baseBackoff time.Duration
}

// NewDispatcher wires a dispatcher. A nil mailer degrades e-mail delivery
// to a logged no-op (webhook channels still work). db is the control-plane
// handle the webhook path resolves the crew egress allowlist from (#1367);
// pass nil only in unit paths with no crew policy to enforce.
func NewDispatcher(lister ChannelLister, mail mailer.Mailer, logger *slog.Logger, db *sql.DB) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	if mail == nil {
		mail = mailer.Disabled{}
	}
	return &Dispatcher{
		lister:      lister,
		mail:        mail,
		scrub:       scrubber.New(),
		db:          db,
		logger:      logger,
		maxAttempts: 3,
		baseBackoff: 200 * time.Millisecond,
	}
}

// webhookClient builds the http.Client for one delivery via the shared gated
// constructor: SSRF-safe transport + a CheckRedirect that re-validates EVERY
// hop against both the SSRF guard and the crew allowlist — a permissive first
// host cannot 3xx-bounce into a private IP or a host outside the crew's
// allowed_domains. Using egresspolicy.Client (rather than hand-rolling the
// CheckRedirect) is what keeps this path in lockstep with hooks / http-steps by
// construction.
func (d *Dispatcher) webhookClient(crewID string) *http.Client {
	return egresspolicy.Client(egresspolicy.DBChecker(d.db, crewID), egresspolicy.Options{
		Timeout:   15 * time.Second,
		Schemes:   []string{"http", "https"},
		Transport: webhookTransport,
	})
}

// Dispatch fans an event out to every enabled channel in the workspace.
// It scrubs and caps the output preview once, then delivers per channel.
// Errors are logged, never returned — this runs off a run's terminal
// path and must not fail it.
func (d *Dispatcher) Dispatch(ctx context.Context, ev NotificationEvent) {
	if ev.WorkspaceID == "" {
		return
	}
	channels, err := d.lister.ListEnabled(ctx, ev.WorkspaceID)
	if err != nil {
		d.logger.Warn("notify: list channels", "error", err, "workspace_id", ev.WorkspaceID)
		return
	}
	if len(channels) == 0 {
		return
	}
	ev.OutputPreview = d.scrubPreview(ev.OutputPreview)
	for _, ch := range channels {
		if !ch.Wants(ev.Type) {
			continue // channel not subscribed to this event type
		}
		if err := d.deliver(ctx, ch, ev); err != nil {
			d.logger.Warn("notify: delivery failed",
				"error", err, "channel_id", ch.ID, "type", ch.Type, "run_id", ev.RunID)
		}
	}
}

// DispatchOne delivers to a single (already-resolved, decrypted) channel.
// Used by the `notifychannel test` path. Errors ARE returned here so the
// CLI can report whether the test send worked.
func (d *Dispatcher) DispatchOne(ctx context.Context, ch Channel, ev NotificationEvent) error {
	ev.OutputPreview = d.scrubPreview(ev.OutputPreview)
	return d.deliver(ctx, ch, ev)
}

// scrubPreview redacts secrets then caps the snippet. The cap is applied
// on a rune boundary so a multi-byte UTF-8 character (emoji, non-Latin
// text — common in agent output) is never sliced mid-rune into invalid
// UTF-8.
func (d *Dispatcher) scrubPreview(s string) string {
	if s == "" {
		return ""
	}
	s = d.scrub.Scrub(s)
	if len(s) > outputPreviewCap {
		// Walk back to the last full rune at/under the byte cap.
		cut := outputPreviewCap
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

func (d *Dispatcher) deliver(ctx context.Context, ch Channel, ev NotificationEvent) error {
	switch ch.Type {
	case ChannelWebhook:
		return d.deliverWebhook(ctx, ch, ev)
	case ChannelEmail:
		return d.deliverEmail(ctx, ch, ev)
	default:
		return fmt.Errorf("notify: unknown channel type %q", ch.Type)
	}
}

// deliverWebhook POSTs the signed payload with retries. Retries on
// transport errors and 5xx/429; a 4xx (other than 429) is a permanent
// client error and is not retried.
func (d *Dispatcher) deliverWebhook(ctx context.Context, ch Channel, ev NotificationEvent) error {
	body, err := json.Marshal(webhookPayload{
		Event:         ev.Type,
		RunID:         ev.RunID,
		Routine:       ev.RoutineSlug,
		Status:        ev.Status,
		OutputPreview: ev.OutputPreview,
		TriggeredBy:   ev.TriggeredBy,
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	sig := "sha256=" + webhook.ComputeHMAC(body, ch.Secret)

	// Crew egress allowlist (#1367): a restricted authoring crew cannot
	// deliver to a host outside its allowed_domains. Checked once here,
	// BEFORE the first request is built or sent — no bytes leave for a
	// blocked destination — and re-checked per redirect hop by
	// webhookClient's CheckRedirect. A policy block is permanent, so it
	// short-circuits the retry loop rather than being retried.
	host := ch.URL
	if u, perr := url.Parse(ch.URL); perr == nil {
		host = u.Host
	}
	if err := egresspolicy.Check(ctx, d.db, ev.AuthorCrewID, host); err != nil {
		return fmt.Errorf("crew egress policy: %w", err)
	}
	client := d.webhookClient(ev.AuthorCrewID)

	var lastErr error
	for attempt := 0; attempt < d.maxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, d.baseBackoff, attempt); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "crewship-notify/1")
		req.Header.Set("X-Crewship-Signature", sig)
		req.Header.Set("X-Crewship-Event", ev.Type)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue // transport error — retry
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("webhook returned %d", resp.StatusCode)
		if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return lastErr // permanent client error — don't retry
		}
	}
	return fmt.Errorf("webhook failed after %d attempts: %w", d.maxAttempts, lastErr)
}

// deliverEmail sends via the instance mailer. A disabled mailer is a
// logged no-op rather than an error — channel creation already rejects
// e-mail when no transport is configured, so this only trips if the
// transport is removed after a channel was created.
func (d *Dispatcher) deliverEmail(ctx context.Context, ch Channel, ev NotificationEvent) error {
	if !d.mail.Configured() {
		d.logger.Info("notify: email channel skipped, mailer not configured", "channel_id", ch.ID)
		return nil
	}
	subject := fmt.Sprintf("[Crewship] routine %s %s", ev.RoutineSlug, ev.Status)
	text := fmt.Sprintf("Routine: %s\nRun: %s\nStatus: %s\n", ev.RoutineSlug, ev.RunID, ev.Status)
	if ev.OutputPreview != "" {
		text += "\nOutput preview:\n" + ev.OutputPreview + "\n"
	}
	htmlBody := "<pre>" + html.EscapeString(text) + "</pre>"

	var lastErr error
	for attempt := 0; attempt < d.maxAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepBackoff(ctx, d.baseBackoff, attempt); err != nil {
				return err
			}
		}
		err := d.mail.Send(ctx, mailer.Message{To: ch.To, Subject: subject, HTML: htmlBody, Text: text})
		if err == nil {
			return nil
		}
		if errors.Is(err, mailer.ErrDisabled) {
			return nil // no transport — treat as no-op
		}
		lastErr = err
	}
	return fmt.Errorf("email failed after %d attempts: %w", d.maxAttempts, lastErr)
}

// sleepBackoff waits baseBackoff * 2^(attempt-1), honoring ctx cancel.
func sleepBackoff(ctx context.Context, base time.Duration, attempt int) error {
	d := base * time.Duration(1<<(attempt-1))
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// EventTypeForStatus maps a terminal run status to a notification event
// type. Returns "" for non-terminal or non-notifying statuses.
func EventTypeForStatus(status string) string {
	switch status {
	case "completed":
		return EventRunCompleted
	case "failed":
		return EventRunFailed
	default:
		return ""
	}
}
