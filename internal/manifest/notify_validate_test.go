package manifest

import (
	"strings"
	"testing"
)

// Notification channels could not be described by a manifest at all, so a demo
// workspace could stand up crews, agents, routines and issues and then have no
// way to say where anything gets delivered. These pin the shape of the new
// block — above all the rule that a secret never lands in the file.

func notifyWorkspaceDoc(chans ...NotificationChannel) *WorkspaceDocument {
	return &WorkspaceDocument{
		APIVersion: "crewship/v1",
		Kind:       "Workspace",
		Metadata:   Metadata{Name: "Demo", Slug: "demo"},
		Spec: WorkspaceSpec{
			NotificationChannels: chans,
			Crews: []CrewSpec{{
				SlugOverride: "ops",
				Name:         "Ops",
				Agents:       []Agent{{Slug: "riley", Name: "Riley"}},
			}},
		},
	}
}

func validateWorkspace(t *testing.T, doc *WorkspaceDocument) error {
	t.Helper()
	b := &Bundle{Workspaces: []WorkspaceDocument{*doc}}
	return b.Validate()
}

func TestNotifyChannel_Validate_HappyPath(t *testing.T) {
	doc := notifyWorkspaceDoc(NotificationChannel{
		Slug:          "eng-alerts",
		Type:          "chat",
		Provider:      "discord",
		FieldsFromEnv: map[string]string{"webhook_url": "DISCORD_WEBHOOK_URL"},
		Categories:    []string{"routines.failed"},
		Agents:        []string{"riley"},
	})
	if err := validateWorkspace(t, doc); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestNotifyChannel_Validate_RejectsAnInlineSecret(t *testing.T) {
	// The whole point of fields_from_env. A webhook URL is a bearer token in
	// URL clothing, and a manifest is a file people commit — so the schema
	// gives literal values nowhere to go, and this pins that a plausible
	// mistake fails loudly rather than being silently ignored.
	doc := notifyWorkspaceDoc(NotificationChannel{
		Slug:     "eng-alerts",
		Type:     "chat",
		Provider: "discord",
		// No fields_from_env at all: nothing to read the webhook from.
	})
	err := validateWorkspace(t, doc)
	if err == nil {
		t.Fatal("expected a chat channel with no field sources to fail validation")
	}
	if !strings.Contains(err.Error(), "fields_from_env") {
		t.Errorf("error should point at fields_from_env, got: %v", err)
	}
}

func TestNotifyChannel_Validate_ChatNeedsAProvider(t *testing.T) {
	doc := notifyWorkspaceDoc(NotificationChannel{
		Slug:          "eng-alerts",
		Type:          "chat",
		FieldsFromEnv: map[string]string{"webhook_url": "X"},
	})
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("want a provider-required error, got %v", err)
	}
}

func TestNotifyChannel_Validate_RejectsUnknownType(t *testing.T) {
	doc := notifyWorkspaceDoc(NotificationChannel{Slug: "x", Type: "carrier-pigeon"})
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("want an unknown-type error naming the value, got %v", err)
	}
}

func TestNotifyChannel_Validate_EmailNeedsARecipient(t *testing.T) {
	doc := notifyWorkspaceDoc(NotificationChannel{Slug: "ops-mail", Type: "email"})
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "to") {
		t.Fatalf("want a recipient-required error, got %v", err)
	}
}

func TestNotifyChannel_Validate_WebhookNeedsAnEnvURL(t *testing.T) {
	// A webhook endpoint usually carries its own token in the path, so it is
	// read from the environment like any other secret.
	doc := notifyWorkspaceDoc(NotificationChannel{Slug: "sink", Type: "webhook"})
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "url_from_env") {
		t.Fatalf("want a url_from_env error, got %v", err)
	}
}

func TestNotifyChannel_Validate_RejectsDuplicateSlugs(t *testing.T) {
	// The slug is the manifest's only identity for a channel; two rows
	// sharing one would make re-applies non-deterministic.
	doc := notifyWorkspaceDoc(
		NotificationChannel{Slug: "dup", Type: "email", To: "a@example.com"},
		NotificationChannel{Slug: "dup", Type: "email", To: "b@example.com"},
	)
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "dup") {
		t.Fatalf("want a duplicate-slug error, got %v", err)
	}
}

func TestNotifyChannel_Validate_RejectsAnUnknownAgent(t *testing.T) {
	// Granting a channel to an agent that the manifest never declares is a
	// typo that would otherwise fail mid-apply, after the channel exists.
	doc := notifyWorkspaceDoc(NotificationChannel{
		Slug: "ops-mail", Type: "email", To: "ops@example.com",
		Agents: []string{"nobody"},
	})
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Fatalf("want an unknown-agent error naming the slug, got %v", err)
	}
}

func TestNotifyChannel_Validate_RejectsAnUnknownCategory(t *testing.T) {
	// The category vocabulary is fixed (taxonomy v2). A typo here would
	// create a channel allowlisted for something that can never fire.
	doc := notifyWorkspaceDoc(NotificationChannel{
		Slug: "ops-mail", Type: "email", To: "ops@example.com",
		Categories: []string{"routines.exploded"},
	})
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "routines.exploded") {
		t.Fatalf("want an unknown-category error, got %v", err)
	}
}

func TestComposioGrant_Validate_RejectsAnUnknownMode(t *testing.T) {
	doc := notifyWorkspaceDoc()
	doc.Spec.Crews[0].Agents[0].ComposioToolkits = []ComposioToolkitGrant{
		{Toolkit: "gmail", Mode: "everything"},
	}
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "everything") {
		t.Fatalf("want an unknown-mode error, got %v", err)
	}
}

func TestComposioGrant_Validate_ToolkitIsRequired(t *testing.T) {
	doc := notifyWorkspaceDoc()
	doc.Spec.Crews[0].Agents[0].ComposioToolkits = []ComposioToolkitGrant{{Mode: "read"}}
	err := validateWorkspace(t, doc)
	if err == nil || !strings.Contains(err.Error(), "toolkit") {
		t.Fatalf("want a toolkit-required error, got %v", err)
	}
}

func TestComposioGrant_Validate_DefaultModeIsAccepted(t *testing.T) {
	// Omitting the mode is normal and means read-only; requiring it would
	// push authors toward writing "full" just to silence a validator.
	doc := notifyWorkspaceDoc()
	doc.Spec.Crews[0].Agents[0].ComposioToolkits = []ComposioToolkitGrant{{Toolkit: "gmail"}}
	if err := validateWorkspace(t, doc); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
