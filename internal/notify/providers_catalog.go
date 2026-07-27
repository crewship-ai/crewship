package notify

import (
	"fmt"
	"net/url"
	"strings"
)

// The provider catalog: what a chat/push destination asks a human for, and
// how those answers become a delivery URL.
//
// # Why a curated catalog rather than the library's own field list
//
// The delivery library describes every service's config through reflection,
// and it is tempting to render that directly. It produces a bad form. Its
// "required" set is about the URL grammar, not about the user: Gotify marks
// an `Extras` MAP required, Slack a `Token` STRUCT, Google Chat three opaque
// path segments. Meanwhile the thing a person actually has in their hand —
// the webhook URL Discord just showed them — is not a field at all; it has
// to be taken apart into two positional URL components.
//
// So the catalog is hand-written: it asks for the artefact the user
// possesses, in our words, and does the taking-apart itself. The library
// stays authoritative for whether the composed URL is valid (see
// ValidateServiceURL), which is what keeps the hand-written parts honest —
// a wrong format fails at compose time, in a test, not on someone's first
// real notification.
//
// Fields marked Secret are never echoed back to a client after creation.

// FieldType tells the UI which control to render.
type FieldType string

const (
	FieldText     FieldType = "text"
	FieldURL      FieldType = "url"
	FieldPassword FieldType = "password"
)

// ProviderField is one input on a provider's form.
type ProviderField struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Secret      bool      `json:"secret,omitempty"`
	Placeholder string    `json:"placeholder,omitempty"`
	// Help is the one-line "where do I find this" the form shows under the
	// input. The single most common reason someone abandons this form is not
	// knowing where a value comes from.
	Help string `json:"help,omitempty"`
	// HelpURL points at the provider's own documentation for obtaining the
	// value. Empty when the provider has no stable page for it.
	HelpURL string `json:"help_url,omitempty"`
}

// ProviderCategory is the section a provider appears under in the catalog.
type ProviderCategory string

const (
	// CategoryChat — team chat rooms. Someone is expected to be reading.
	CategoryChat ProviderCategory = "chat"
	// CategoryPush — a notification on a device. Reaches one person, now.
	CategoryPush ProviderCategory = "push"
	// CategoryIncident — on-call routing. Escalates and pages until ack'd.
	CategoryIncident ProviderCategory = "incident"
)

// CategoryInfo names a catalog section for display.
type CategoryInfo struct {
	Key   ProviderCategory `json:"key"`
	Label string           `json:"label"`
	// Hint is the one-line "what is this bucket for" the section header
	// carries — the difference between chat and incident is a routing
	// decision, not a cosmetic one.
	Hint string `json:"hint"`
}

// ProviderCategories returns the catalog sections in display order.
//
// Order is deliberate and matches how teams adopt them: chat first (what most
// workspaces already have), then push (reaches an individual), then incident
// (the escalation path you set up once and hope never fires).
func ProviderCategories() []CategoryInfo {
	return []CategoryInfo{
		{Key: CategoryChat, Label: "Chat", Hint: "Posts into a team room — good for anything a group should see"},
		{Key: CategoryPush, Label: "Push", Hint: "A notification on someone's device — reaches one person immediately"},
		{Key: CategoryIncident, Label: "Incident", Hint: "On-call routing that escalates until someone acknowledges"},
	}
}

// ProviderSpec is one destination type a channel can deliver to: its form
// fields and how they compose into a delivery URL.
type ProviderSpec struct {
	// Name is the stable identifier stored on the channel row and used by
	// the API and CLI.
	Name string `json:"name"`
	// Label is what a person sees.
	Label string `json:"label"`
	// Blurb is a one-line description for the picker.
	Blurb string `json:"blurb"`
	// Category groups the provider in the catalog. Assigned here rather than
	// in the frontend so adding a provider cannot silently land it in a
	// default bucket — see providers_category_test.go.
	Category ProviderCategory `json:"category"`
	// Scheme is the delivery library's URL scheme. Internal — never shown.
	Scheme string `json:"-"`
	// Fields are the form inputs, in display order.
	Fields []ProviderField `json:"fields"`

	// compose turns validated field values into a delivery URL.
	compose func(map[string]string) (string, error) `json:"-"`
}

// FieldByKey returns the named field, or nil.
func (p ProviderSpec) FieldByKey(key string) *ProviderField {
	for i := range p.Fields {
		if p.Fields[i].Key == key {
			return &p.Fields[i]
		}
	}
	return nil
}

// providerCatalog is the ordered, curated set. Order is display order in the
// picker: the destinations teams most commonly connect come first.
var providerCatalog = []ProviderSpec{
	{
		Name:     ProviderDiscord,
		Label:    "Discord",
		Blurb:    "Post to a Discord channel through a webhook",
		Scheme:   "discord",
		Category: CategoryChat,
		Fields: []ProviderField{
			{
				Key: "webhook_url", Label: "Webhook URL", Type: FieldURL, Required: true, Secret: true,
				Placeholder: "https://discord.com/api/webhooks/123456789/abcdef...",
				Help:        "Discord → Server Settings → Integrations → Webhooks → New Webhook, then Copy Webhook URL.",
				HelpURL:     "https://support.discord.com/hc/en-us/articles/228383668",
			},
			{
				Key: "bot_name", Label: "Bot display name", Type: FieldText,
				Placeholder: "Crewship",
				Help:        "Optional. Overrides the name the webhook posts under.",
			},
		},
		compose: composeDiscord,
	},
	{
		Name:     ProviderSlack,
		Label:    "Slack",
		Blurb:    "Post to a Slack channel through an incoming webhook",
		Scheme:   "slack",
		Category: CategoryChat,
		Fields: []ProviderField{
			{
				Key: "webhook_url", Label: "Incoming webhook URL", Type: FieldURL, Required: true, Secret: true,
				Placeholder: "https://hooks.slack.com/services/T0000/B0000/XXXXXXXX",
				Help:        "Slack → your app → Incoming Webhooks → Add New Webhook to Workspace, then copy the URL.",
				HelpURL:     "https://api.slack.com/messaging/webhooks",
			},
			{
				Key: "bot_name", Label: "Bot display name", Type: FieldText,
				Placeholder: "Crewship",
				Help:        "Optional. Overrides the name messages are posted under.",
			},
		},
		compose: composeSlack,
	},
	{
		Name:     ProviderTelegram,
		Label:    "Telegram",
		Blurb:    "Send to a Telegram chat, group, or channel",
		Scheme:   "telegram",
		Category: CategoryChat,
		Fields: []ProviderField{
			{
				Key: "bot_token", Label: "Bot token", Type: FieldPassword, Required: true, Secret: true,
				Placeholder: "123456789:AAExampleTokenFromBotFather",
				Help:        "Message @BotFather on Telegram, send /newbot, and copy the token it gives you.",
				HelpURL:     "https://core.telegram.org/bots/features#botfather",
			},
			{
				Key: "chat_id", Label: "Chat ID", Type: FieldText, Required: true,
				Placeholder: "@my-channel or 123456789",
				Help:        "A channel name with the @ prefix, or a numeric chat ID. Add the bot to the chat first, or it cannot post.",
			},
		},
		compose: composeTelegram,
	},
	{
		Name:     ProviderNtfy,
		Label:    "ntfy",
		Blurb:    "Publish to an ntfy topic (self-hosted or ntfy.sh)",
		Scheme:   "ntfy",
		Category: CategoryPush,
		Fields: []ProviderField{
			{
				Key: "topic", Label: "Topic", Type: FieldText, Required: true,
				Placeholder: "crewship-alerts",
				Help:        "The topic name subscribers listen on. Anyone who knows a public topic name can read it — pick something unguessable.",
				HelpURL:     "https://docs.ntfy.sh/publish/",
			},
			{
				Key: "server", Label: "Server", Type: FieldText,
				Placeholder: "ntfy.sh",
				Help:        "Optional. Your own ntfy host; defaults to ntfy.sh.",
			},
			{
				Key: "username", Label: "Username", Type: FieldText,
				Help: "Optional. Only needed if the topic requires authentication.",
			},
			{
				Key: "password", Label: "Password", Type: FieldPassword, Secret: true,
				Help: "Optional. Only needed if the topic requires authentication.",
			},
		},
		compose: composeNtfy,
	},
	{
		Name:     ProviderGotify,
		Label:    "Gotify",
		Blurb:    "Push to a self-hosted Gotify server",
		Scheme:   "gotify",
		Category: CategoryPush,
		Fields: []ProviderField{
			{
				Key: "server", Label: "Server", Type: FieldText, Required: true,
				Placeholder: "gotify.example.com",
				Help:        "Host of your Gotify server, with a port if it is not on 443.",
			},
			{
				Key: "app_token", Label: "Application token", Type: FieldPassword, Required: true, Secret: true,
				Placeholder: "A1b2C3d4E5f6G7h",
				Help:        "Gotify → Apps → create an application, then copy its token.",
				HelpURL:     "https://gotify.net/docs/pushmsg",
			},
		},
		compose: composeGotify,
	},
	{
		Name:     ProviderPushover,
		Label:    "Pushover",
		Blurb:    "Push to your phone via Pushover",
		Scheme:   "pushover",
		Category: CategoryPush,
		Fields: []ProviderField{
			{
				Key: "user_key", Label: "User key", Type: FieldText, Required: true, Secret: true,
				Placeholder: "uQiRzpo4DXghDmr9QzzfQu27cmVRsG",
				Help:        "Shown on your Pushover dashboard right after you log in.",
				HelpURL:     "https://pushover.net/",
			},
			{
				Key: "api_token", Label: "Application token", Type: FieldPassword, Required: true, Secret: true,
				Placeholder: "azGDORePK8gMaC0QOYAMyEEuzJnyUi",
				Help:        "Pushover → Create an Application/API Token, then copy the token.",
				HelpURL:     "https://pushover.net/apps/build",
			},
		},
		compose: composePushover,
	},
	{
		Name:     ProviderMattermost,
		Label:    "Mattermost",
		Blurb:    "Post to a Mattermost channel through a webhook",
		Scheme:   "mattermost",
		Category: CategoryChat,
		Fields: []ProviderField{
			{
				Key: "server", Label: "Server", Type: FieldText, Required: true,
				Placeholder: "mattermost.example.com",
				Help:        "Host of your Mattermost server, with a port if it is not on 443.",
			},
			{
				Key: "token", Label: "Webhook token", Type: FieldPassword, Required: true, Secret: true,
				Placeholder: "xxxxxxxxxxxxxxxxxxxxxxxxxx",
				Help:        "Integrations → Incoming Webhooks → Add. The token is the last path segment of the URL it gives you.",
				HelpURL:     "https://developers.mattermost.com/integrate/webhooks/incoming/",
			},
			{
				Key: "channel", Label: "Channel", Type: FieldText,
				Placeholder: "town-square",
				Help:        "Optional. Overrides the channel the webhook was created for.",
			},
		},
		compose: composeMattermost,
	},
	{
		Name:     ProviderMatrix,
		Label:    "Matrix",
		Blurb:    "Send to a Matrix room",
		Scheme:   "matrix",
		Category: CategoryChat,
		Fields: []ProviderField{
			{
				Key: "server", Label: "Homeserver", Type: FieldText, Required: true,
				Placeholder: "matrix.org",
				Help:        "Host of the homeserver the account lives on.",
			},
			{
				Key: "access_token", Label: "Access token", Type: FieldPassword, Required: true, Secret: true,
				Help:    "Element → Settings → Help & About → Access Token. Treat it like a password: it grants full account access.",
				HelpURL: "https://spec.matrix.org/latest/client-server-api/#client-authentication",
			},
			{
				Key: "rooms", Label: "Rooms", Type: FieldText,
				Placeholder: "#alerts:matrix.org",
				Help:        "Optional. Comma-separated rooms to post in; defaults to every room the account has joined.",
			},
		},
		compose: composeMatrix,
	},
	{
		Name:     ProviderTeams,
		Label:    "Microsoft Teams",
		Blurb:    "Post to a Teams channel through a Power Automate workflow",
		Scheme:   "teams",
		Category: CategoryChat,
		Fields: []ProviderField{
			{
				Key: "webhook_url", Label: "Workflow URL", Type: FieldURL, Required: true, Secret: true,
				Placeholder: "https://prod-00.westeurope.logic.azure.com:443/workflows/...",
				Help:        "Teams channel → Workflows → 'Post to a channel when a webhook request is received'. Copy the URL the flow gives you.",
				HelpURL:     "https://support.microsoft.com/en-us/office/create-incoming-webhooks-with-workflows-for-microsoft-teams-8ae491c7-0394-4861-ba59-055e33f75498",
			},
		},
		compose: composeTeams,
	},
	{
		Name:     ProviderGoogleChat,
		Label:    "Google Chat",
		Blurb:    "Post to a Google Chat space through a webhook",
		Scheme:   "googlechat",
		Category: CategoryChat,
		Fields: []ProviderField{
			{
				Key: "webhook_url", Label: "Webhook URL", Type: FieldURL, Required: true, Secret: true,
				Placeholder: "https://chat.googleapis.com/v1/spaces/AAA/messages?key=...&token=...",
				Help:        "Google Chat space → Apps & integrations → Webhooks → Add webhooks, then copy the URL.",
				HelpURL:     "https://developers.google.com/chat/how-tos/webhooks",
			},
		},
		compose: composeGoogleChat,
	},
	{
		Name:     ProviderOpsgenie,
		Label:    "Opsgenie",
		Blurb:    "Raise an Opsgenie alert",
		Scheme:   "opsgenie",
		Category: CategoryIncident,
		Fields: []ProviderField{
			{
				Key: "api_key", Label: "API key", Type: FieldPassword, Required: true, Secret: true,
				Help:    "Opsgenie → Teams → your team → Integrations → add an API integration, then copy its key.",
				HelpURL: "https://support.atlassian.com/opsgenie/docs/create-a-default-api-integration/",
			},
			{
				Key: "server", Label: "API host", Type: FieldText,
				Placeholder: "api.opsgenie.com",
				Help:        "Optional. Use api.eu.opsgenie.com for EU-hosted accounts.",
			},
		},
		compose: composeOpsgenie,
	},
}

// Provider name constants. These are stored on the channel row, so they are
// API surface — renaming one is a breaking change.
const (
	ProviderNtfy       = "ntfy"
	ProviderGotify     = "gotify"
	ProviderPushover   = "pushover"
	ProviderMattermost = "mattermost"
	ProviderMatrix     = "matrix"
	ProviderTeams      = "teams"
	ProviderGoogleChat = "googlechat"
	ProviderOpsgenie   = "opsgenie"
)

// providersByName indexes the catalog.
var providersByName = func() map[string]ProviderSpec {
	m := make(map[string]ProviderSpec, len(providerCatalog))
	for _, p := range providerCatalog {
		m[p.Name] = p
	}
	return m
}()

// Providers returns the catalog in display order.
func Providers() []ProviderSpec { return providerCatalog }

// ProviderByName resolves a provider, reporting whether it exists.
func ProviderByName(name string) (ProviderSpec, bool) {
	p, ok := providersByName[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// SupportedProviders lists the provider names, in catalog order.
func SupportedProviders() []string {
	out := make([]string, 0, len(providerCatalog))
	for _, p := range providerCatalog {
		out = append(out, p.Name)
	}
	return out
}

// ComposeServiceURL turns a provider's form values into a delivery URL and
// verifies the result actually parses as that service. The user never sees or
// types this string.
//
// Validation is not a formality: the composers take webhook URLs apart by
// hand, and a provider that changes its URL shape would otherwise produce a
// channel that saves cleanly and silently fails on its first real send.
func ComposeServiceURL(providerName string, fields map[string]string) (string, error) {
	p, ok := ProviderByName(providerName)
	if !ok {
		return "", fmt.Errorf("notify: unknown provider %q (want one of %v)", providerName, SupportedProviders())
	}
	trimmed := make(map[string]string, len(fields))
	for k, v := range fields {
		trimmed[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	for _, f := range p.Fields {
		if f.Required && trimmed[f.Key] == "" {
			return "", fmt.Errorf("notify: %s needs %s", p.Label, f.Label)
		}
	}
	raw, err := p.compose(trimmed)
	if err != nil {
		return "", err
	}
	if err := ValidateServiceURL(raw); err != nil {
		return "", fmt.Errorf("notify: %s settings did not produce a usable delivery target: %w", p.Label, err)
	}
	return raw, nil
}

// ── composers ───────────────────────────────────────────────────────────────
//
// Each takes the artefact a user actually has and produces the delivery URL.

// composeDiscord splits a Discord webhook URL
// (https://discord.com/api/webhooks/<id>/<token>) into its two parts.
//
// The `webhooks` segment is required rather than just taking the last two
// path components. A channel link — https://discord.com/channels/<guild>/<id>
// — has the same shape, and it is the thing people reach for first because it
// is what the Discord UI gives you when you right-click a channel. Without
// this check that link composes into a syntactically valid delivery URL that
// saves cleanly and never delivers anything.
func composeDiscord(f map[string]string) (string, error) {
	const want = "https://discord.com/api/webhooks/<id>/<token>"
	if !hasPathSegment(f["webhook_url"], "webhooks") {
		return "", fmt.Errorf("notify: that looks like a Discord channel link, not a webhook URL "+
			"(expected %s — Server Settings → Integrations → Webhooks → Copy Webhook URL)", want)
	}
	id, token, err := splitTrailingPair(f["webhook_url"], "Discord webhook URL", want)
	if err != nil {
		return "", err
	}
	u := "discord://" + url.PathEscape(token) + "@" + url.PathEscape(id)
	if name := f["bot_name"]; name != "" {
		u += "?username=" + url.QueryEscape(name)
	}
	return u, nil
}

// composeSlack turns an incoming-webhook URL
// (https://hooks.slack.com/services/T…/B…/…) into the hook token triple.
func composeSlack(f map[string]string) (string, error) {
	raw := f["webhook_url"]
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("notify: Slack incoming webhook URL is not a valid URL " +
			"(expected https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXX)")
	}
	parts := splitPath(u.Path)
	// .../services/A/B/C — take the last three segments so a URL with or
	// without the /services prefix both work.
	if len(parts) < 3 {
		return "", fmt.Errorf("notify: Slack incoming webhook URL is missing its token parts " +
			"(expected https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXX)")
	}
	// The delivery library expects the three segments dash-joined. That means
	// a segment containing a dash would compose into a token the library
	// re-splits wrongly — Slack does not issue such segments, and if it ever
	// did, ComposeServiceURL's validation step rejects the result at create
	// time rather than saving a channel that silently never delivers.
	tail := parts[len(parts)-3:]
	return "slack://hook:" + strings.Join(tail, "-") + "@webhook", nil
}

func composeTelegram(f map[string]string) (string, error) {
	chat := f["chat_id"]
	return "telegram://" + url.PathEscape(f["bot_token"]) +
		"@telegram?chats=" + url.QueryEscape(chat), nil
}

func composeNtfy(f map[string]string) (string, error) {
	host := f["server"]
	if host == "" {
		host = "ntfy.sh"
	}
	auth := ""
	if user := f["username"]; user != "" {
		auth = url.PathEscape(user)
		if pass := f["password"]; pass != "" {
			auth += ":" + url.PathEscape(pass)
		}
		auth += "@"
	}
	return "ntfy://" + auth + host + "/" + url.PathEscape(f["topic"]), nil
}

func composeGotify(f map[string]string) (string, error) {
	return "gotify://" + stripScheme(f["server"]) + "/" + url.PathEscape(f["app_token"]), nil
}

func composePushover(f map[string]string) (string, error) {
	return "pushover://:" + url.PathEscape(f["api_token"]) + "@" + url.PathEscape(f["user_key"]), nil
}

func composeMattermost(f map[string]string) (string, error) {
	u := "mattermost://" + stripScheme(f["server"]) + "/" + url.PathEscape(f["token"])
	if ch := f["channel"]; ch != "" {
		u += "/" + url.PathEscape(ch)
	}
	return u, nil
}

func composeMatrix(f map[string]string) (string, error) {
	u := "matrix://:" + url.PathEscape(f["access_token"]) + "@" + stripScheme(f["server"])
	if rooms := f["rooms"]; rooms != "" {
		u += "?rooms=" + url.QueryEscape(rooms)
	}
	return u, nil
}

// composeTeams passes the Power Automate workflow URL through as the `host`
// parameter, which is the shape this service expects — there is nothing to
// take apart.
func composeTeams(f map[string]string) (string, error) {
	raw := f["webhook_url"]
	if _, err := url.Parse(raw); err != nil {
		return "", fmt.Errorf("notify: Teams workflow URL is not a valid URL")
	}
	return "teams://?host=" + url.QueryEscape(stripScheme(raw)), nil
}

// composeGoogleChat carries the whole webhook URL, which already contains the
// space, key and token as its own query parameters.
func composeGoogleChat(f map[string]string) (string, error) {
	raw := f["webhook_url"]
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("notify: Google Chat webhook URL is not a valid URL")
	}
	return "googlechat://" + u.Host + u.Path + "?" + u.RawQuery, nil
}

func composeOpsgenie(f map[string]string) (string, error) {
	host := f["server"]
	if host == "" {
		host = "api.opsgenie.com"
	}
	return "opsgenie://" + stripScheme(host) + "/" + url.PathEscape(f["api_key"]), nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// splitTrailingPair pulls the last two path segments out of a webhook URL,
// the shape "…/<id>/<token>" that several providers use.
func splitTrailingPair(raw, label, want string) (first, second string, err error) {
	u, perr := url.Parse(raw)
	if perr != nil || u.Host == "" {
		return "", "", fmt.Errorf("notify: %s is not a valid URL (expected %s)", label, want)
	}
	parts := splitPath(u.Path)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("notify: %s is missing its id and token (expected %s)", label, want)
	}
	return parts[len(parts)-2], parts[len(parts)-1], nil
}

// hasPathSegment reports whether raw parses as a URL whose path contains the
// given segment.
func hasPathSegment(raw, segment string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	for _, s := range splitPath(u.Path) {
		if s == segment {
			return true
		}
	}
	return false
}

func splitPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// stripScheme removes a leading http(s):// and any trailing slash, so a user
// who pastes a full URL into a "server" field gets what they meant.
func stripScheme(s string) string {
	s = strings.TrimSpace(s)
	for _, prefix := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, prefix)
	}
	return strings.TrimRight(s, "/")
}
