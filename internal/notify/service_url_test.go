package notify

import (
	"context"
	"strings"
	"testing"
)

// Discord arrived as four separate boxes: the title in one, each body line in
// another, the link in a third. shoutrrr's Discord service defaults
// splitLines=Yes — "send each line as a separate embedded item" — so a
// perfectly ordinary multi-line message was fragmented into a stack of
// disconnected quotes, and a notification carrying a JSON payload looked like
// a malfunction rather than a result.
//
// The fix is applied at DELIVERY, not when a channel is composed. A channel's
// service URL is stored encrypted at creation time, so fixing composeDiscord
// alone would leave every channel anyone already made broken with no way to
// repair it short of deleting and re-adding it.

func TestServiceURLForDelivery_DiscordSendsOneMessage(t *testing.T) {
	got := ServiceURLForDelivery("discord://token@channelid")
	if !strings.Contains(got, "splitLines=No") {
		t.Errorf("Discord must send one message, got %q", got)
	}
}

func TestServiceURLForDelivery_KeepsExistingQuery(t *testing.T) {
	// A composed URL may already carry username= from the channel's bot_name.
	got := ServiceURLForDelivery("discord://token@channelid?username=Crewship")
	if !strings.Contains(got, "username=Crewship") {
		t.Errorf("existing options must survive, got %q", got)
	}
	if !strings.Contains(got, "splitLines=No") {
		t.Errorf("want splitLines added alongside, got %q", got)
	}
}

func TestServiceURLForDelivery_RespectsAnExplicitChoice(t *testing.T) {
	// An operator who deliberately set splitLines keeps it. This function
	// supplies a default, it does not overrule.
	got := ServiceURLForDelivery("discord://token@channelid?splitLines=Yes")
	if strings.Contains(got, "splitLines=No") {
		t.Errorf("an explicit splitLines must not be overwritten, got %q", got)
	}
}

func TestServiceURLForDelivery_LeavesOtherProvidersAlone(t *testing.T) {
	// splitLines is a Discord option. Appending it to a service that does
	// not know it makes shoutrrr reject the whole URL as an unknown key,
	// which would turn a cosmetic improvement into a delivery outage.
	for _, raw := range []string{
		"slack://hook:token@webhook",
		"telegram://token@telegram?chats=123",
		"ntfy://ntfy.sh/topic",
		"",
	} {
		if got := ServiceURLForDelivery(raw); got != raw {
			t.Errorf("ServiceURLForDelivery(%q) = %q, want it untouched", raw, got)
		}
	}
}

func TestServiceURLForDelivery_LeavesAnUnparseableURLAlone(t *testing.T) {
	// Better to deliver a fragmented message than to mangle a URL and
	// deliver nothing.
	const broken = "discord://%zz"
	if got := ServiceURLForDelivery(broken); got != broken {
		t.Errorf("got %q, want the input untouched", got)
	}
}

func TestDeliverCategoryMessage_AppliesTheDeliveryURL(t *testing.T) {
	// The seam that matters: a channel stored before this existed must still
	// get the option, because the normalisation happens on the way out.
	fp := &fakeProvider{}
	defer SetProviderForTesting(fp)()

	d := fastDispatcher(t, staticLister{}, nil)
	ch := Channel{ID: "c1", Type: ChannelShoutrrr, Secret: "discord://token@chan", Enabled: true}

	if err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w", Category: CategoryRoutinesCompleted, Title: "Done",
	}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := fp.calls()[0].URL; !strings.Contains(got, "splitLines=No") {
		t.Errorf("delivery url = %q, want the Discord option applied", got)
	}
}
