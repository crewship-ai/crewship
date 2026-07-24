package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"

	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// CategoryMessage is a preference-routed notification (issue #1412) —
// deliberately a DISTINCT shape from NotificationEvent (the original
// run-terminal broadcast, issue #850) so the wire format any external
// receiver already depends on for run.completed/run.failed never changes,
// while the new category × channel matrix gets a payload purpose-built for
// "you have an approval waiting" / "your agent replied" / etc, rather than
// repurposing RunID/Routine/Status fields that don't fit those events.
type CategoryMessage struct {
	WorkspaceID string
	Category    string // one of notify.AllCategories
	Title       string
	Body        string // markdown/plain body; scrubbed + capped by the caller
	Priority    string // low|medium|high|urgent
	SourceKind  string // e.g. inbox kind: waitpoint|escalation|failed_run|message|memory_consolidation
	SourceID    string
	URL         string // optional deep link (e.g. chat url)
}

// categoryWebhookPayload is the JSON POSTed to a webhook channel for a
// category-routed message, HMAC-signed exactly like webhookPayload.
type categoryWebhookPayload struct {
	Category   string `json:"category"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Priority   string `json:"priority,omitempty"`
	SourceKind string `json:"source_kind,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
	URL        string `json:"url,omitempty"`
}

// DeliverCategoryMessage sends msg to ch. It supports every channel type
// (email/webhook/shoutrrr) so a category can fan out to whatever channel a
// user or admin configured — this is the delivery half of the #1412
// preference router; the router (internal/notifyroute) decides WHETHER to
// call this, this function only decides HOW.
func (d *Dispatcher) DeliverCategoryMessage(ctx context.Context, ch Channel, msg CategoryMessage) error {
	msg.Body = d.scrubPreview(msg.Body)
	switch ch.Type {
	case ChannelWebhook:
		return d.deliverCategoryWebhook(ctx, ch, msg)
	case ChannelEmail:
		return d.deliverCategoryEmail(ctx, ch, msg)
	case ChannelShoutrrr:
		return d.deliverCategoryShoutrrr(ctx, ch, msg)
	default:
		return fmt.Errorf("notify: unknown channel type %q", ch.Type)
	}
}

func (d *Dispatcher) deliverCategoryWebhook(ctx context.Context, ch Channel, msg CategoryMessage) error {
	body, err := json.Marshal(categoryWebhookPayload{
		Category: msg.Category, Title: msg.Title, Body: msg.Body,
		Priority: msg.Priority, SourceKind: msg.SourceKind, SourceID: msg.SourceID, URL: msg.URL,
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	sig := "sha256=" + webhook.ComputeHMAC(body, ch.Secret)

	// Category-routed pushes have no authoring crew (they originate from a
	// user/system inbox event, not a routine run), so the crew egress
	// allowlist gate is skipped — matches NotificationEvent.AuthorCrewID's
	// documented "empty = no crew scope" degrade. The SSRF guard
	// (webhookClient's SafeTransport) still applies unconditionally.
	client := d.webhookClient("")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "crewship-notify/1")
	req.Header.Set("X-Crewship-Signature", sig)
	req.Header.Set("X-Crewship-Category", msg.Category)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

func (d *Dispatcher) deliverCategoryEmail(ctx context.Context, ch Channel, msg CategoryMessage) error {
	if !d.mail.Configured() {
		return mailer.ErrDisabled
	}
	subject := fmt.Sprintf("[Crewship] %s", msg.Title)
	text := msg.Body
	if msg.URL != "" {
		text += "\n\n" + msg.URL
	}
	htmlBody := "<pre>" + html.EscapeString(text) + "</pre>"
	return d.mail.Send(ctx, mailer.Message{To: ch.To, Subject: subject, HTML: htmlBody, Text: text})
}

func (d *Dispatcher) deliverCategoryShoutrrr(ctx context.Context, ch Channel, msg CategoryMessage) error {
	if ch.Secret == "" {
		return fmt.Errorf("notify: shoutrrr channel %s has no service url", ch.ID)
	}
	message := msg.Title
	if msg.Body != "" {
		message += "\n\n" + msg.Body
	}
	if msg.URL != "" {
		message += "\n" + msg.URL
	}
	return provider.Send(ctx, ch.Secret, message)
}
