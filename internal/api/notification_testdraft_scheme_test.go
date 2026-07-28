package api

import (
	"strings"
	"testing"
)

// TestDraft accepted a pre-composed shoutrrr_url verbatim.
//
// ChannelStore.Create validates a raw URL against the provider's own scheme
// (validateShoutrrrURL(rawURL, p.Scheme)); this endpoint skipped that whenever
// `fields` was empty. Since the only thing it checked was that the PROVIDER
// NAME was admin-enabled, a caller could name an enabled provider and hand
// over a URL for an entirely different service:
//
//	{"type":"shoutrrr","provider":"discord",
//	 "shoutrrr_url":"generic://10.0.0.7:8500/v1/kv/admin"}
//
// shoutrrr resolves `generic` to an HTTP POST and `smtp` to a mail relay, so
// the server becomes an outbound requester aimed wherever the caller likes —
// reachable by any authenticated member, and persisting nothing, so no
// channel row records that it happened. Both the admin provider allowlist and
// the provider-to-scheme binding were bypassed on this path alone.

func TestTestDraft_RawURLMustMatchTheNamedProvider(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	for _, raw := range []string{
		"generic://10.0.0.7:8500/v1/kv/admin",
		"smtp://user:pass@internal-relay:25/?from=a@b.c&to=d@e.f",
		"logger://",
	} {
		rr := postDraft(t, h, "ADMIN", `{
			"type": "shoutrrr",
			"provider": "discord",
			"shoutrrr_url": "`+raw+`"
		}`)
		if rr.Code != 400 {
			t.Errorf("%s: got %d, want 400 — the scheme does not belong to discord and must never be dispatched",
				raw, rr.Code)
			continue
		}
		if !strings.Contains(rr.Body.String(), "discord") {
			t.Errorf("%s: the error should name the provider whose scheme was expected, got: %s",
				raw, rr.Body.String())
		}
	}
}

func TestTestDraft_RawURLForTheNamedProviderStillWorks(t *testing.T) {
	// The endpoint's legitimate use — an operator pasting a delivery URL they
	// already hold — must keep working. 400 here would mean the fix went too
	// far; a delivery failure (no real Discord) is the expected outcome.
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	rr := postDraft(t, h, "ADMIN", `{
		"type": "shoutrrr",
		"provider": "discord",
		"shoutrrr_url": "discord://tokenvalue@123456789012345678"
	}`)
	if rr.Code == 400 {
		t.Fatalf("a matching raw URL was rejected before dispatch: %s", rr.Body.String())
	}
}

func TestTestDraft_ComposedFieldsAreUnaffected(t *testing.T) {
	// Composition already produces a URL with the provider's own scheme, so
	// the added check must be a no-op on that path rather than a second
	// chance to fail.
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	rr := postDraft(t, h, "ADMIN", `{
		"type": "shoutrrr",
		"provider": "discord",
		"fields": {"webhook_url": "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOp"}
	}`)
	if rr.Code == 400 {
		t.Fatalf("composed draft rejected: %s", rr.Body.String())
	}
}
