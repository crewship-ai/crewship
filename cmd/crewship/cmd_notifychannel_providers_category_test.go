package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// TestProvidersListShowsCategory pins that `notifychannel providers` surfaces
// the catalog section.
//
// Without it the CLI prints every destination as one undifferentiated list,
// and the operator has no way to tell that Opsgenie pages an on-call rota
// while Discord posts into a room — a routing decision, not a cosmetic one.
// The dashboard's Catalog tab groups by exactly this field, so showing it here
// is what keeps "every API endpoint gets a CLI command" meaningful: the two
// clients describe the same catalog rather than one of them knowing more.
func TestProvidersListShowsCategory(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/notification-providers", clitest.JSONResponse(200, map[string]any{
		"providers": []map[string]any{
			{"provider": "discord", "label": "Discord", "category": "chat", "enabled": true,
				"fields": []map[string]any{{"key": "webhook_url", "required": true}}},
			{"provider": "opsgenie", "label": "Opsgenie", "category": "incident", "enabled": true,
				"fields": []map[string]any{{"key": "api_key", "required": true}}},
		},
		"categories": []map[string]any{
			{"key": "chat", "label": "Chat"},
			{"key": "incident", "label": "Incident"},
		},
	}))

	var runErr error
	out := covCaptureStdoutCli9(t, func() {
		runErr = notifyChannelProvidersCmd.RunE(notifyChannelProvidersCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("providers: %v", runErr)
	}

	if !strings.Contains(out, "CATEGORY") {
		t.Errorf("providers table has no CATEGORY column:\n%s", out)
	}
	if !strings.Contains(out, "chat") {
		t.Errorf("discord's category (chat) is missing from the table:\n%s", out)
	}
	if !strings.Contains(out, "incident") {
		t.Errorf("opsgenie's category (incident) is missing from the table:\n%s", out)
	}
}

// TestProviderDetailShowsCategory pins the same for the single-provider view,
// where the category is what tells you whether this destination is the right
// KIND of place for what you are about to route to it.
func TestProviderDetailShowsCategory(t *testing.T) {
	s := covStubCli9(t)
	s.OnGet("/api/v1/notification-providers", clitest.JSONResponse(200, map[string]any{
		"providers": []map[string]any{
			{"provider": "opsgenie", "label": "Opsgenie", "blurb": "Raise an Opsgenie alert",
				"category": "incident", "enabled": true,
				"fields": []map[string]any{
					{"key": "api_key", "label": "API key", "required": true, "help": "Opsgenie → Teams → Integrations."},
				}},
		},
	}))
	covSetFlagCli9(t, notifyChannelProvidersCmd, "provider", "opsgenie")

	var runErr error
	out := covCaptureStdoutCli9(t, func() {
		runErr = notifyChannelProvidersCmd.RunE(notifyChannelProvidersCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("providers --provider opsgenie: %v", runErr)
	}
	if !strings.Contains(out, "incident") {
		t.Errorf("provider detail does not name the category:\n%s", out)
	}
}
