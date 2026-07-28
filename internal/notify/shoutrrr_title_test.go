package notify

import (
	"context"
	"strings"
	"testing"
)

// Every chat and push service shoutrrr exposes has a native title field, and
// we used none of them: the title was glued onto the front of the message text
// and sent as one blob. On a chat service that is cosmetic — a bold header
// instead of a plain first line. On a PUSH service it is not: Pushover, ntfy
// and Gotify put the title on the lock screen, so a phone notification showed
// the first line of the body where the title belonged.
//
// Ten of the eleven providers in our catalog read a title param. googlechat is
// the one that does not — its Send signature discards params entirely — so for
// that one, and only that one, the title has to stay in the text or it would
// vanish.

func TestShoutrrrMessage_TitleTravelsAsAParam(t *testing.T) {
	for _, scheme := range []string{
		"discord://t@c", "telegram://t@telegram?chats=1", "slack://a:b@c",
		"ntfy://ntfy.sh/topic", "gotify://host/token", "pushover://shoutrrr:key@user",
		"mattermost://host/token", "matrix://user:pass@host", "teams://a@b/c/d",
		"opsgenie://host/token",
	} {
		msg, params := shoutrrrMessage(scheme, "Run failed", "the details")
		if params["title"] != "Run failed" {
			t.Errorf("%s: title should ride as a param, got %v", scheme, params)
		}
		if strings.Contains(msg, "Run failed") {
			t.Errorf("%s: title must not ALSO be in the body, or it prints twice:\n%s", scheme, msg)
		}
		if !strings.Contains(msg, "the details") {
			t.Errorf("%s: body went missing:\n%s", scheme, msg)
		}
	}
}

func TestShoutrrrMessage_GoogleChatKeepsTheTitleInTheText(t *testing.T) {
	// googlechat's Send takes `_ *types.Params` — it throws them away. Moving
	// the title out of the text would silently drop it there.
	msg, params := shoutrrrMessage("googlechat://chat.googleapis.com/v1/spaces/x", "Run failed", "the details")
	if params["title"] != "" {
		t.Errorf("googlechat ignores params; setting one is misleading: %v", params)
	}
	if !strings.Contains(msg, "Run failed") || !strings.Contains(msg, "the details") {
		t.Errorf("both title and body must survive in the text:\n%s", msg)
	}
}

func TestShoutrrrMessage_NeverSendsAnEmptyMessage(t *testing.T) {
	// A title-only notification is normal (a journal entry with no facts).
	// Moving the title to a param would leave the message empty, and an empty
	// message is a delivery a service can reject outright.
	msg, params := shoutrrrMessage("discord://t@c", "Run failed", "")
	if strings.TrimSpace(msg) == "" {
		t.Error("message must not be empty; a service may refuse it")
	}
	if params["title"] != "Run failed" {
		t.Errorf("the title should still be a param, got %v", params)
	}
}

func TestShoutrrrMessage_NoTitleIsFine(t *testing.T) {
	msg, params := shoutrrrMessage("discord://t@c", "", "just a body")
	if _, ok := params["title"]; ok {
		t.Errorf("an empty title must not be sent as a param: %v", params)
	}
	if !strings.Contains(msg, "just a body") {
		t.Errorf("body went missing:\n%s", msg)
	}
}

func TestDeliverCategoryMessage_SendsTheTitleNatively(t *testing.T) {
	fp := &fakeProvider{}
	defer SetProviderForTesting(fp)()

	d := fastDispatcher(t, staticLister{}, nil).WithPublicURL("https://x.test")
	ch := Channel{ID: "c1", Type: ChannelShoutrrr, Secret: "discord://t@c", Enabled: true}

	if err := d.DeliverCategoryMessage(context.Background(), ch, CategoryMessage{
		WorkspaceID: "w", Category: CategoryRoutinesFailed,
		Title: "Run failed", Body: "step fetch errored",
		Links: []Link{{Label: "Open runs", Path: "/runs"}},
	}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	call := fp.calls()[0]
	if call.Params["title"] != "Run failed" {
		t.Errorf("title = %v, want it as a native param", call.Params)
	}
	if strings.Contains(call.Message, "Run failed") {
		t.Errorf("title duplicated into the body:\n%s", call.Message)
	}
	for _, want := range []string{"step fetch errored", "https://x.test/runs"} {
		if !strings.Contains(call.Message, want) {
			t.Errorf("message is missing %q:\n%s", want, call.Message)
		}
	}
}

func TestDispatch_LegacyShoutrrrAlsoSendsATitle(t *testing.T) {
	// The run-terminal broadcast builds its own summary line and had the same
	// problem. One rule, both paths — otherwise the two producers drift and
	// only one of them ever gets fixed.
	fp := &fakeProvider{}
	defer SetProviderForTesting(fp)()

	ch := Channel{ID: "c1", Type: ChannelShoutrrr, Secret: "discord://t@c", Enabled: true}
	d := fastDispatcher(t, staticLister{[]Channel{ch}}, nil)

	d.Dispatch(context.Background(), NotificationEvent{
		Type: EventRunFailed, WorkspaceID: "w", RunID: "run_1",
		RoutineSlug: "nightly", Status: "failed", OutputPreview: "boom",
	})

	calls := fp.calls()
	if len(calls) != 1 {
		t.Fatalf("want 1 send, got %d", len(calls))
	}
	if calls[0].Params["title"] == "" {
		t.Errorf("the legacy path should title its message too, got %v", calls[0].Params)
	}
	if !strings.Contains(calls[0].Message, "boom") {
		t.Errorf("output preview went missing:\n%s", calls[0].Message)
	}
}
