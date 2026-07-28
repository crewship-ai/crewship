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

	// Links are the deep links a recipient acts on — the issue, the run,
	// the journal entry. App-relative; delivery makes them absolute. Order
	// is meaningful: the first is the primary action, and it is the one
	// single-URL formats carry (see PrimaryLink).
	Links []Link

	// Vars are the source's own facts — run_id, issue_identifier,
	// routine_slug, agent_name, status. They exist so a message can be
	// TEMPLATED: a template can only reference what the envelope carries,
	// so without this, automatic notifications are unparameterisable by
	// construction rather than by omission.
	//
	// Scrubbed recursively at delivery along with everything else.
	Vars map[string]any
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
	// URL keeps meaning exactly what it meant before links existed — the
	// primary deep link — so a receiver already parsing it does not break.
	URL   string                `json:"url,omitempty"`
	Links []categoryLinkPayload `json:"links,omitempty"`
	Vars  map[string]any        `json:"vars,omitempty"`
}

// categoryLinkPayload is a Link on the wire. Path becomes `url` because by
// the time it is serialised it has been made absolute — calling it "path"
// would describe the internal form, not what the receiver gets.
type categoryLinkPayload struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
}

// DeliverCategoryMessage sends msg to ch. It supports every channel type
// (email/webhook/shoutrrr) so a category can fan out to whatever channel a
// user or admin configured — this is the delivery half of the #1412
// preference router; the router (internal/notifyroute) decides WHETHER to
// call this, this function only decides HOW.
func (d *Dispatcher) DeliverCategoryMessage(ctx context.Context, ch Channel, msg CategoryMessage) error {
	// One scrub, covering the whole envelope, for every producer. The body
	// additionally keeps its length cap.
	scrubMessage(&msg, d.scrubText)
	msg.Body = capPreview(msg.Body)
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
	links := msg.resolveLinks(d.publicURL)
	wire := make([]categoryLinkPayload, 0, len(links))
	for _, l := range links {
		wire = append(wire, categoryLinkPayload{Label: l.Label, URL: l.Path})
	}
	var primary string
	if len(links) > 0 {
		primary = links[0].Path
	}
	body, err := json.Marshal(categoryWebhookPayload{
		Category: msg.Category, Title: msg.Title, Body: msg.Body,
		Priority: msg.Priority, SourceKind: msg.SourceKind, SourceID: msg.SourceID,
		URL: primary, Links: wire, Vars: msg.Vars,
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
	if lines := linkLines(msg.resolveLinks(d.publicURL)); lines != "" {
		if text != "" {
			text += "\n\n"
		}
		text += lines
	}
	htmlBody := "<pre>" + html.EscapeString(text) + "</pre>"
	return d.mail.Send(ctx, mailer.Message{To: ch.To, Subject: subject, HTML: htmlBody, Text: text})
}

func (d *Dispatcher) deliverCategoryShoutrrr(ctx context.Context, ch Channel, msg CategoryMessage) error {
	if ch.Secret == "" {
		return fmt.Errorf("notify: shoutrrr channel %s has no service url", ch.ID)
	}
	body := msg.Body
	if lines := linkLines(msg.resolveLinks(d.publicURL)); lines != "" {
		if body != "" {
			body += "\n\n"
		}
		body += lines
	}
	url := ServiceURLForDelivery(ch.Secret)
	message, params := shoutrrrMessage(url, msg.Title, body)
	return provider.Send(ctx, url, message, params)
}
