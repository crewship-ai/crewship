package manifest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped demo manifest is the file people copy. If it stops parsing, or
// silently loses a block, everyone who starts from it inherits the gap — so
// it is loaded and planned here rather than only being tried by hand.

func loadDemoManifest(t *testing.T) *Bundle {
	t.Helper()
	path := filepath.Join("..", "..", "examples", "manifests", "demo.workspace.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read demo manifest: %v", err)
	}
	b, err := Load(data)
	if err != nil {
		t.Fatalf("load demo manifest: %v", err)
	}
	return b
}

func TestDemoManifest_ParsesEveryBlock(t *testing.T) {
	b := loadDemoManifest(t)

	if len(b.Workspaces) != 1 {
		t.Fatalf("want 1 workspace document, got %d", len(b.Workspaces))
	}
	ws := b.Workspaces[0]

	// The block that silently vanished the first time this was run against a
	// real server: the plan showed crews, agents, the routine and the issue,
	// and no notification channel at all — neither created nor skipped.
	if len(ws.Spec.NotificationChannels) != 1 {
		t.Fatalf("notification_channels did not parse: got %d", len(ws.Spec.NotificationChannels))
	}
	ch := ws.Spec.NotificationChannels[0]
	if ch.Provider != "discord" || ch.FieldsFromEnv["webhook_url"] != "DISCORD_WEBHOOK_URL" {
		t.Errorf("channel parsed wrong: %+v", ch)
	}
	// demo-riley, not riley: agent slugs are unique per WORKSPACE, so a demo
	// applied beside an existing seed collides on a plain name.
	if len(ch.Agents) != 1 || ch.Agents[0] != "demo-riley" {
		t.Errorf("the agent grant did not parse: %v", ch.Agents)
	}

	if ws.Spec.Composio == nil || ws.Spec.Composio.APIKeyFromEnv != "COMPOSIO_API_KEY" {
		t.Errorf("composio block did not parse: %+v", ws.Spec.Composio)
	}

	// The link the demo exists to show.
	if len(b.Issues) != 1 {
		t.Fatalf("want 1 issue document, got %d", len(b.Issues))
	}
	if got := b.Issues[0].Spec.RoutineSlug; got != "demo-fetch-and-report" {
		t.Errorf("issue is not bound to the routine: routine_slug = %q", got)
	}
	if len(b.Routines) != 1 {
		t.Fatalf("want 1 routine document, got %d", len(b.Routines))
	}

	// Composio grants ride on the agent.
	agents := ws.Spec.Crews[0].Agents
	if len(agents) != 1 || len(agents[0].ComposioToolkits) != 1 {
		t.Fatalf("composio_toolkits did not parse: %+v", agents)
	}
	if agents[0].ComposioToolkits[0].Toolkit != "gmail" {
		t.Errorf("grant toolkit = %q, want gmail", agents[0].ComposioToolkits[0].Toolkit)
	}
}

func TestDemoManifest_Validates(t *testing.T) {
	if err := loadDemoManifest(t).Validate(); err != nil {
		t.Fatalf("the shipped demo manifest must validate: %v", err)
	}
}

func TestDemoManifest_PlansTheChannelWhenSecretsAreSupplied(t *testing.T) {
	b := loadDemoManifest(t)
	plan, err := BuildPlan(context.Background(), NewClient(newFakeAPI(t)), b, Options{
		Secrets: MapSecretsSource{
			"DISCORD_WEBHOOK_URL": "https://discord.com/api/webhooks/1/abc",
			"COMPOSIO_API_KEY":    "ck_test",
		},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	// No composio_grant here on purpose: the demo's grant carries no
	// user_id, so it is reported as skipped rather than planned. Supplying
	// the API key does not change that — the key configures the workspace,
	// the user_id names a connected account nobody has connected.
	for _, want := range []struct{ kind, needle string }{
		{"notification_channel", "demo-alerts"},
		{"notification_grant", "demo-alerts:demo-riley"},
		{"composio", "api-key"},
	} {
		if findPlanItem(plan, want.kind, want.needle) == nil {
			t.Errorf("plan is missing %s %q; got %s", want.kind, want.needle, planKinds(plan))
		}
	}
}

func TestDemoManifest_ReportsWhatItSkippedWithoutSecrets(t *testing.T) {
	// Running `apply` without --secrets-file is the likeliest first mistake.
	// It must not fail, and it must say what it left out and which variable
	// to fill — silence here is how someone concludes the feature is broken.
	b := loadDemoManifest(t)
	plan, err := BuildPlan(context.Background(), NewClient(newFakeAPI(t)), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan without secrets must still succeed: %v", err)
	}
	// Two: the channel with no webhook URL, and the Composio grant with no
	// connected account. Asserted by content rather than by count so adding
	// a third skippable thing to the demo does not fail this for the wrong
	// reason.
	joinedSkips := strings.Join(plan.Skipped, " | ")
	for _, want := range []string{"DISCORD_WEBHOOK_URL", "user_id"} {
		if !strings.Contains(joinedSkips, want) {
			t.Errorf("the skip list must name %q, got %v", want, plan.Skipped)
		}
	}
	joined := strings.Join(plan.Warnings, " | ")
	if !strings.Contains(joined, "COMPOSIO_API_KEY") {
		t.Errorf("want a warning naming the Composio variable, got %v", plan.Warnings)
	}
}
