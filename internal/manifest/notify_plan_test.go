package manifest

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Planning the notification block. The behaviour that matters most is the one
// about secrets: a value only ever comes from the CredentialSource, and a
// channel whose value is missing is SKIPPED rather than created half-built —
// an enabled channel pointing nowhere is worse than an absent one, because it
// looks configured.

// PlanItem carries no slug — identity in a plan is (kind, description) — so
// the helpers below match on the description containing the slug, which is
// also what an operator reads off the printed plan.
func findPlanItem(p *Plan, kind, needle string) *PlanItem {
	for i := range p.Items {
		if p.Items[i].Kind == kind && strings.Contains(p.Items[i].Description, needle) {
			return &p.Items[i]
		}
	}
	return nil
}

func planKinds(p *Plan) string {
	out := make([]string, 0, len(p.Items))
	for _, it := range p.Items {
		out = append(out, it.Kind+":"+it.Description)
	}
	return strings.Join(out, " | ")
}

func planFor(t *testing.T, b *Bundle, opts Options) (*Plan, error) {
	t.Helper()
	if opts.Secrets == nil {
		opts.Secrets = NoSecretsSource{}
	}
	return BuildPlan(context.Background(), NewClient(newFakeAPI(t)), b, opts)
}

func notifyPlanDoc(chans ...NotificationChannel) *Bundle {
	return &Bundle{Workspaces: []WorkspaceDocument{{
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
	}}}
}

func discordChannel() NotificationChannel {
	return NotificationChannel{
		Slug:          "eng-alerts",
		Type:          "chat",
		Provider:      "discord",
		FieldsFromEnv: map[string]string{"webhook_url": "DISCORD_WEBHOOK_URL"},
		Categories:    []string{"routines.failed"},
	}
}

func TestNotifyPlan_CreatesAChannelWhenTheSecretIsAvailable(t *testing.T) {
	b := notifyPlanDoc(discordChannel())
	secrets := MapSecretsSource{"DISCORD_WEBHOOK_URL": "https://discord.com/api/webhooks/1/abc"}

	plan, err := planFor(t, b, Options{Secrets: secrets})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	item := findPlanItem(plan, "notification_channel", "eng-alerts")
	if item == nil {
		t.Fatalf("no plan item for the channel; got %s", planKinds(plan))
	}
	if item.Action != ActionCreate {
		t.Errorf("action = %s, want create", item.Action)
	}
}

func TestNotifyPlan_SkipsAChannelWhoseSecretIsMissing(t *testing.T) {
	// No secrets source at all — the common case when someone runs `apply`
	// without --from-env. Creating the channel anyway would produce a row
	// that is enabled, looks configured, and delivers nowhere.
	b := notifyPlanDoc(discordChannel())

	plan, err := planFor(t, b, Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if item := findPlanItem(plan, "notification_channel", "eng-alerts"); item != nil {
		t.Errorf("channel was planned despite having no value: %s", item.Description)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0], "DISCORD_WEBHOOK_URL") {
		t.Errorf("the skip must be reported and name the missing variable, got %v", plan.Skipped)
	}
}

func TestNotifyPlan_EmailNeedsNoSecret(t *testing.T) {
	// An address is not a credential, so an email channel applies with no
	// secrets source at all.
	b := notifyPlanDoc(NotificationChannel{Slug: "ops-mail", Type: "email", To: "ops@example.com"})

	plan, err := planFor(t, b, Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findPlanItem(plan, "notification_channel", "ops-mail") == nil {
		t.Errorf("email channel should plan without secrets; got %s", planKinds(plan))
	}
}

func TestNotifyPlan_ComposioKeyIsPlannedOnlyWhenSupplied(t *testing.T) {
	b := notifyPlanDoc()
	b.Workspaces[0].Spec.Composio = &ComposioSpec{APIKeyFromEnv: "COMPOSIO_API_KEY"}

	withoutKey, err := planFor(t, b, Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findPlanItem(withoutKey, "composio", "api-key") != nil {
		t.Error("planned a Composio key with nothing to set it to")
	}

	withKey, err := planFor(t, b, Options{Secrets: MapSecretsSource{"COMPOSIO_API_KEY": "ck_live_x"}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findPlanItem(withKey, "composio", "api-key") == nil {
		t.Errorf("expected a Composio key item; got %s", planKinds(withKey))
	}
}

func TestNotifyPlan_NeverPutsTheSecretInTheDescription(t *testing.T) {
	// The plan is printed to a terminal and often pasted into a ticket.
	b := notifyPlanDoc(discordChannel())
	secret := "https://discord.com/api/webhooks/1/SUPERSECRET"

	plan, err := planFor(t, b, Options{Secrets: MapSecretsSource{"DISCORD_WEBHOOK_URL": secret}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, it := range plan.Items {
		if strings.Contains(it.Description, "SUPERSECRET") {
			t.Fatalf("plan line leaks the secret: %s", it.Description)
		}
	}
}

func TestNotifyPlan_GrantsAreSeparateItems(t *testing.T) {
	// A pairing is its own operation: it can fail on its own (the channel's
	// own authority gate), and folding it into the create would make that
	// failure look like the channel failed.
	ch := discordChannel()
	ch.Agents = []string{"riley"}
	b := notifyPlanDoc(ch)

	plan, err := planFor(t, b, Options{Secrets: MapSecretsSource{"DISCORD_WEBHOOK_URL": "https://x/y"}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if findPlanItem(plan, "notification_grant", "eng-alerts:riley") == nil {
		t.Errorf("expected a separate grant item; got %s", planKinds(plan))
	}
}

func TestNotifyPlan_AnEmptyEnvValueCountsAsMissing(t *testing.T) {
	// The shape people actually hit: the variable is DECLARED in .env.local
	// but left blank. Treating a present-but-empty value as supplied would
	// create a Discord channel whose webhook is the empty string — enabled,
	// looking configured, delivering nowhere. Both secret sources agree on
	// this, so the placeholder behaves the same whether it comes from the
	// process environment or a --secrets-file.
	b := notifyPlanDoc(discordChannel())

	for name, src := range map[string]CredentialSource{
		"secrets-file": MapSecretsSource{"DISCORD_WEBHOOK_URL": ""},
		"from-env":     EnvSecretsSource{Lookup: func(string) (string, bool) { return "", true }},
	} {
		t.Run(name, func(t *testing.T) {
			plan, err := planFor(t, b, Options{Secrets: src})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if findPlanItem(plan, "notification_channel", "eng-alerts") != nil {
				t.Error("a blank value must not create the channel")
			}
			if len(plan.Skipped) != 1 {
				t.Fatalf("want the channel reported as skipped, got %v", plan.Skipped)
			}
			if !strings.Contains(plan.Skipped[0], "DISCORD_WEBHOOK_URL") {
				t.Errorf("the skip must name the variable to fill in, got %q", plan.Skipped[0])
			}
		})
	}
}

func TestComposioGrant_MissingUserIDIsPendingNotFatal(t *testing.T) {
	// The bind endpoint rejects an empty user_id, and the user id is the
	// CONNECTED ACCOUNT's identity — instance-specific, created by a browser
	// flow no manifest can perform. So a portable file that omits it is
	// describing a grant that cannot be made yet, which is the same condition
	// as "no account connected" and must not fail the apply.
	for _, msg := range []string{
		"API error (400): user_id is required",
		"no connected account for gmail",
		"agent is not connected to that toolkit",
	} {
		if !composioNeedsAccount(errors.New(msg)) {
			t.Errorf("%q should be treated as pending, not as a failure", msg)
		}
	}
	// A genuine failure still is one — otherwise every mistake would be
	// silently downgraded to a warning.
	if composioNeedsAccount(errors.New("API error (500): internal")) {
		t.Error("a server error must not be swallowed as pending")
	}
}
