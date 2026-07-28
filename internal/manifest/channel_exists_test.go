package manifest

import (
	"context"
	"strings"
	"testing"
)

// planNotificationChannel checked for a missing secret BEFORE looking for the
// channel on the server, and returned early. So re-running `apply` without
// --from-env against a workspace where the channel already exists reported
// "notification channel X (needs SLACK_WEBHOOK_URL)" and planned nothing else
// — the agent grants and the category preferences declared alongside it were
// never reached.
//
// The message reads as "the channel was not created", so an operator adding
// an agent to an existing channel sees a warning about a variable, concludes
// it did not matter, and only finds out later when the agent's notify_send
// comes back 403 not-paired.
//
// The secret is only needed to CREATE a channel. Matching an existing one uses
// type, provider and to-address — all declared in the manifest — which is why
// matchRemoteChannel can run first. The destination of an existing channel is
// deliberately never patched anyway ("re-sending a secret that already works
// is churn with a blast radius"), so nothing on that path wants the value.

func channelManifestWithGrants(t *testing.T) *Bundle {
	t.Helper()
	b, err := Load([]byte(`
apiVersion: crewship/v1
kind: Workspace
metadata:
  name: Test
  slug: ws-test
spec:
  notification_channels:
    - slug: eng-alerts
      type: chat
      provider: slack
      fields_from_env:
        webhook_url: SLACK_WEBHOOK_URL
      categories:
        - routines.failed
      deliver_to_me:
        - routines.failed
      agents:
        - riley
  crews:
    - slug: eng
      name: Engineering
      agents:
        - slug: riley
          name: Riley
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return b
}

func TestPlan_ExistingChannelStillGetsItsGrantsWithoutTheSecret(t *testing.T) {
	b := channelManifestWithGrants(t)
	api := newFakeAPI(t)
	api.agentsBySlug["riley"] = map[string]any{"id": "ag_1", "slug": "riley"}
	// The channel is already there from an earlier apply that DID have the
	// secret. Matching uses type + provider, neither of which is secret.
	api.notifyChannels = []map[string]any{
		{"id": "nch_1", "type": "shoutrrr", "provider": "slack", "scope": "workspace",
			"categories": []string{"routines.failed"}, "enabled": true},
	}

	plan, err := BuildPlan(context.Background(), NewClient(api), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if findPlanItem(plan, "notification_grant", "eng-alerts:riley") == nil {
		t.Errorf("the agent grant was dropped because the secret was absent; got %s", planKinds(plan))
	}
	if findPlanItem(plan, "notification_pref", "eng-alerts → me") == nil {
		t.Errorf("the delivery preference was dropped too; got %s", planKinds(plan))
	}
	// And nothing is "skipped": the channel exists, so no variable was needed.
	for _, s := range plan.Skipped {
		if strings.Contains(s, "eng-alerts") {
			t.Errorf("an existing channel must not be reported as skipped: %q", s)
		}
	}
}

func TestPlan_AbsentChannelWithoutTheSecretIsStillSkipped(t *testing.T) {
	// The original behaviour, for the case it was actually about: the channel
	// does not exist and cannot be created without its destination.
	b := channelManifestWithGrants(t)
	api := newFakeAPI(t)
	api.agentsBySlug["riley"] = map[string]any{"id": "ag_1", "slug": "riley"}

	plan, err := BuildPlan(context.Background(), NewClient(api), b, Options{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	joined := strings.Join(plan.Skipped, " | ")
	if !strings.Contains(joined, "SLACK_WEBHOOK_URL") {
		t.Errorf("want the missing variable named, got %v", plan.Skipped)
	}
	if findPlanItem(plan, "notification_grant", "eng-alerts:riley") != nil {
		t.Error("a grant on a channel that will not exist must not be planned")
	}
}

func TestPlan_ChannelListFailureIsNotSilentlyEmpty(t *testing.T) {
	// planNotifications discarded every error from ListNotificationChannels
	// and carried on with remote = nil, so a transient 500 or timeout made
	// every declared channel plan as a CREATE. Re-running apply during a blip
	// duplicated the workspace's Slack, Discord and webhook channels, and
	// channels have no slug to reconcile them by afterwards.
	b := channelManifestWithGrants(t)
	api := newFakeAPI(t)
	api.failChannelList = true

	_, err := BuildPlan(context.Background(), NewClient(api), b, Options{
		Secrets: MapSecretsSource{"SLACK_WEBHOOK_URL": "https://hooks.slack.com/services/T0/B0/XXX"},
	})
	if err == nil {
		t.Fatal("a failed channel list must abort the plan, not read as 'no channels exist'")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "channel") {
		t.Errorf("the error should say what could not be listed, got: %v", err)
	}
}
