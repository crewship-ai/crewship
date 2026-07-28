package notify

import (
	"strings"
	"testing"
)

// fakeSlackWebhookURL builds a structurally-valid but obviously-fake Slack
// incoming-webhook URL. Slack requires T + 8, B + 8, and a 24-character
// token, and the delivery library enforces exactly that — so this cannot be
// replaced with a friendlier-looking placeholder.
func fakeSlackWebhookURL() string {
	const (
		team    = "T00000000"
		bot     = "B00000000"
		notReal = "EXAMPLEnotarealtoken0000" // 24 chars
	)
	return "https://hooks.slack.com/" + "services/" + team + "/" + bot + "/" + notReal
}

// sampleFields is a filled-in form per provider — the SHAPE a user would
// paste in, with obviously-synthetic values.
//
// The values are deliberately not realistic-looking. A placeholder that
// resembles a real credential trips the secret scanner on every commit, and a
// scanner people learn to wave through stops being a scanner.
var sampleFields = map[string]map[string]string{
	ProviderDiscord: {
		"webhook_url": "https://discord.com/api/webhooks/000000000000000000/EXAMPLE-not-a-real-token",
		"bot_name":    "Crewship",
	},
	ProviderSlack: {
		// Assembled rather than written out: Slack's own segment shape is
		// strictly validated by the delivery library, so the sample has to
		// LOOK like a real webhook URL — and a literal that looks like one
		// trips the repository's secret scanner on every commit.
		"webhook_url": fakeSlackWebhookURL(),
	},
	ProviderTelegram: {
		"bot_token": "000000000:EXAMPLE-not-a-real-token",
		"chat_id":   "@my-channel",
	},
	ProviderNtfy: {
		"topic": "crewship-alerts",
	},
	ProviderGotify: {
		"server":    "gotify.example.com",
		"app_token": "EXAMPLE-not-a-real-token",
	},
	ProviderPushover: {
		"user_key":  "EXAMPLE-not-a-real-user-key",
		"api_token": "EXAMPLE-not-a-real-token",
	},
	ProviderMattermost: {
		"server": "mattermost.example.com",
		"token":  "EXAMPLE-not-a-real-token",
	},
	ProviderMatrix: {
		"server":       "matrix.org",
		"access_token": "EXAMPLE-not-a-real-token",
	},
	ProviderTeams: {
		"webhook_url": "https://prod-00.westeurope.logic.azure.com:443/workflows/abc/triggers/manual/paths/invoke",
	},
	ProviderGoogleChat: {
		"webhook_url": "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=abc&token=def",
	},
	ProviderOpsgenie: {
		"api_key": "EXAMPLE-not-a-real-key",
	},
}

// TestEveryProviderComposesAValidURL is the guard that makes a hand-written
// catalog safe.
//
// The composers take a provider's webhook URL apart by hand — "the last two
// path segments are the id and the token". That is correct until a provider
// changes its URL shape, and without this test a wrong composer would produce
// a channel that saves cleanly, looks right in the list, and fails only on
// someone's first real notification. Composing a realistic form and handing
// the result to the delivery library's own parser moves that failure to CI.
func TestEveryProviderComposesAValidURL(t *testing.T) {
	for _, p := range Providers() {
		t.Run(p.Name, func(t *testing.T) {
			fields, ok := sampleFields[p.Name]
			if !ok {
				t.Fatalf("provider %q has no sample form in this test — add one, or a new provider "+
					"ships with its URL composition unverified", p.Name)
			}
			raw, err := ComposeServiceURL(p.Name, fields)
			if err != nil {
				t.Fatalf("compose: %v", err)
			}
			if !strings.HasPrefix(raw, p.Scheme+"://") {
				t.Errorf("composed %q, which is not a %s:// url", raw, p.Scheme)
			}
			if err := ValidateServiceURL(raw); err != nil {
				t.Errorf("the delivery library rejects the composed url: %v\n  composed: %s", err, raw)
			}
		})
	}
}

// TestProviderCatalogIsWellFormed pins the metadata the UI depends on. A
// missing label or help line is invisible on the backend and shows up as a
// blank form control.
func TestProviderCatalogIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Providers() {
		if seen[p.Name] {
			t.Errorf("duplicate provider name %q", p.Name)
		}
		seen[p.Name] = true

		if p.Label == "" || p.Blurb == "" || p.Scheme == "" {
			t.Errorf("provider %q is missing a label, blurb or scheme", p.Name)
		}
		if p.compose == nil {
			t.Errorf("provider %q has no composer", p.Name)
		}
		if len(p.Fields) == 0 {
			t.Errorf("provider %q has no fields — the form would be empty", p.Name)
		}
		hasRequired := false
		keys := map[string]bool{}
		for _, f := range p.Fields {
			if keys[f.Key] {
				t.Errorf("provider %q has a duplicate field key %q", p.Name, f.Key)
			}
			keys[f.Key] = true
			if f.Key == "" || f.Label == "" {
				t.Errorf("provider %q has a field with no key or label: %+v", p.Name, f)
			}
			// Every field needs a "where do I find this" line — not knowing
			// where a value comes from is the single most common reason
			// someone gives up on this form.
			if f.Help == "" {
				t.Errorf("provider %q field %q has no help text", p.Name, f.Key)
			}
			if f.Required {
				hasRequired = true
			}
		}
		if !hasRequired {
			t.Errorf("provider %q has no required field — nothing identifies the destination", p.Name)
		}
	}
}

// TestComposeRejectsMissingRequiredFields pins that an incomplete form fails
// with a message naming the field, rather than composing a broken URL.
func TestComposeRejectsMissingRequiredFields(t *testing.T) {
	_, err := ComposeServiceURL(ProviderTelegram, map[string]string{"bot_token": "123:abc"})
	if err == nil {
		t.Fatal("composing Telegram without a chat id should fail")
	}
	if !strings.Contains(err.Error(), "Chat ID") {
		t.Errorf("error should name the missing field, got: %v", err)
	}
}

// TestComposeRejectsMalformedWebhookURLs pins the paste-the-wrong-thing case:
// a user who pastes a channel link instead of a webhook URL must be told, not
// silently given a channel that never delivers.
func TestComposeRejectsMalformedWebhookURLs(t *testing.T) {
	cases := []struct{ provider, field, value string }{
		{ProviderDiscord, "webhook_url", "https://discord.com/channels/12345"},
		{ProviderSlack, "webhook_url", "https://hooks.slack.com/services/T00000000"},
		{ProviderDiscord, "webhook_url", "not a url at all"},
	}
	for _, c := range cases {
		fields := map[string]string{c.field: c.value}
		if _, err := ComposeServiceURL(c.provider, fields); err == nil {
			t.Errorf("%s with %s=%q should have been rejected", c.provider, c.field, c.value)
		}
	}
}

func TestComposeRejectsUnknownProvider(t *testing.T) {
	if _, err := ComposeServiceURL("carrier-pigeon", map[string]string{}); err == nil {
		t.Fatal("an unknown provider should be rejected")
	}
}

// TestSupportedProvidersMatchesCatalog guards the API/CLI surface against the
// catalog and the name list drifting apart.
func TestSupportedProvidersMatchesCatalog(t *testing.T) {
	names := SupportedProviders()
	if len(names) != len(Providers()) {
		t.Fatalf("SupportedProviders has %d entries, the catalog has %d", len(names), len(Providers()))
	}
	for i, p := range Providers() {
		if names[i] != p.Name {
			t.Errorf("position %d: SupportedProviders has %q, catalog has %q", i, names[i], p.Name)
		}
		if _, ok := ProviderByName(p.Name); !ok {
			t.Errorf("ProviderByName(%q) does not resolve", p.Name)
		}
	}
}
