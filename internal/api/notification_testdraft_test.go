package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// postDraft drives POST /api/v1/notification-channels/test with the given
// body as the named role.
func postDraft(t *testing.T, h *NotifyChannelHandler, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/notification-channels/test", strings.NewReader(body)),
		"u1", "ws1", role)
	rr := httptest.NewRecorder()
	h.TestDraft(rr, req)
	return rr
}

// TestTestDraft_ComposesFromProviderFields pins the point of the endpoint:
// a user fills in the provider's form and can verify it BEFORE saving.
// Previously the only test endpoint required a persisted channel, so the
// first confirmation that a pasted webhook URL was right came after
// committing it.
//
// The dispatch itself is expected to fail here — there is no real Discord on
// the other end — but it must fail at DELIVERY, not at composition, and it
// must not have persisted anything.
func TestTestDraft_ComposesFromProviderFields(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	rr := postDraft(t, h, "ADMIN", `{
		"type": "shoutrrr",
		"provider": "discord",
		"fields": {"webhook_url": "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOp"}
	}`)

	// 502 = composed fine, delivery failed (no real receiver). 400 would mean
	// we never got past composition, which is the regression this guards.
	if rr.Code == 400 {
		t.Fatalf("draft was rejected before dispatch — composition failed: %s", rr.Body.String())
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_channels`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a draft test persisted %d channel row(s); it must save nothing", n)
	}
}

// TestTestDraft_RejectsBadFieldsWithAUsefulMessage pins that a mistyped
// webhook URL is reported as a form problem, naming what went wrong, rather
// than surfacing as an opaque delivery failure the user cannot act on.
func TestTestDraft_RejectsBadFieldsWithAUsefulMessage(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	// A Discord CHANNEL link rather than a webhook URL — the thing the
	// Discord UI hands you when you right-click a channel, and therefore the
	// mistake people actually make.
	rr := postDraft(t, h, "ADMIN", `{
		"type": "shoutrrr",
		"provider": "discord",
		"fields": {"webhook_url": "https://discord.com/channels/123/456"}
	}`)
	if rr.Code != 400 {
		t.Fatalf("expected 400 for a channel link, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "webhook") {
		t.Errorf("the error should explain what was expected, got: %s", rr.Body.String())
	}
}

// TestTestDraft_RejectsEmptyDraft pins that submitting nothing is a form
// error rather than an attempted send to an empty destination.
func TestTestDraft_RejectsEmptyDraft(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	rr := postDraft(t, h, "ADMIN", `{"type": "shoutrrr", "provider": "telegram", "fields": {}}`)
	if rr.Code != 400 {
		t.Fatalf("expected 400 for an empty form, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestTestDraft_RejectsUnknownType guards the switch's default arm.
func TestTestDraft_RejectsUnknownType(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	rr := postDraft(t, h, "ADMIN", `{"type": "carrier-pigeon"}`)
	if rr.Code != 400 {
		t.Fatalf("expected 400 for an unknown channel type, got %d", rr.Code)
	}
}

// TestTestDraft_RejectsDisabledProvider pins that the instance-wide provider
// toggle is honoured on this path too. Without the check, a provider an admin
// switched off instance-wide could still be exercised through the draft test.
func TestTestDraft_RejectsDisabledProvider(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(
		`INSERT INTO app_settings (key, value) VALUES (?, 'false')`,
		providerSettingKey("discord")); err != nil {
		t.Fatal(err)
	}
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	rr := postDraft(t, h, "ADMIN", `{
		"type": "shoutrrr",
		"provider": "discord",
		"fields": {"webhook_url": "https://discord.com/api/webhooks/123456789012345678/AbCdEfGhIjKlMnOp"}
	}`)
	if rr.Code != 400 {
		t.Fatalf("expected 400 for a disabled provider, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "disabled") {
		t.Errorf("the error should say the provider is disabled, got: %s", rr.Body.String())
	}
}

// TestTestDraft_EmailRequiresConfiguredMailer pins the fail-closed posture
// Create already takes: an email draft on an instance with no mail transport
// is rejected up front rather than reported as a mysterious delivery failure.
func TestTestDraft_EmailRequiresConfiguredMailer(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger()) // nil mailer → Disabled

	rr := postDraft(t, h, "ADMIN", `{"type": "email", "to": "ops@example.com"}`)
	if rr.Code != 400 {
		t.Fatalf("expected 400 when no mailer is configured, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestTestDraft_RequiresAuthentication pins that an unauthenticated caller
// cannot make the server send traffic to a destination they supply.
func TestTestDraft_RequiresAuthentication(t *testing.T) {
	db := setupTestDB(t)
	h := NewNotifyChannelHandler(db, nil, newTestLogger())

	// Workspace context but no user on it.
	req := httptest.NewRequest("POST", "/api/v1/notification-channels/test",
		strings.NewReader(`{"type":"webhook","url":"https://example.com/hook"}`))
	req = req.WithContext(withWorkspace(req.Context(), "ws1", "ADMIN"))
	rr := httptest.NewRecorder()
	h.TestDraft(rr, req)

	if rr.Code != 401 {
		t.Fatalf("expected 401 without an authenticated user, got %d: %s", rr.Code, rr.Body.String())
	}
}
