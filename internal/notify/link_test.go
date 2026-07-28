package notify

import "testing"

// A notification carried ONE opaque `URL` string, and exactly one producer
// ever set it — the chat bridge, from a RELATIVE path:
//
//	chatURL := "/chat/" + agentSlug + "?session=" + chatID
//
// Delivered verbatim to Discord, that is not a link. A chat client has no
// origin to resolve it against, so the single clickable thing a notification
// could carry has never worked anywhere except the app's own inbox.
//
// Links are therefore stored app-relative — the canonical form every producer
// can build without knowing where the server happens to be reachable — and
// made absolute ONCE, at delivery, which is the only layer that knows the
// instance's public URL. Producers never think about base URLs.

func TestAbsoluteLink_MakesAnAppRelativePathClickable(t *testing.T) {
	got := AbsoluteLink("https://crewship.example.com", "/issues/CS-12")
	want := "https://crewship.example.com/issues/CS-12"
	if got != want {
		t.Errorf("AbsoluteLink() = %q, want %q", got, want)
	}
}

func TestAbsoluteLink_JoinsExactlyOneSlash(t *testing.T) {
	// A public URL pasted with a trailing slash is the likeliest way to
	// configure this, and "…com//issues/CS-12" is a broken link on enough
	// servers to be worth pinning.
	for _, tc := range []struct{ base, path, want string }{
		{"https://x.test/", "/issues/CS-12", "https://x.test/issues/CS-12"},
		{"https://x.test", "issues/CS-12", "https://x.test/issues/CS-12"},
		{"https://x.test/", "issues/CS-12", "https://x.test/issues/CS-12"},
		{"https://x.test/crewship/", "/issues/CS-12", "https://x.test/crewship/issues/CS-12"},
	} {
		if got := AbsoluteLink(tc.base, tc.path); got != tc.want {
			t.Errorf("AbsoluteLink(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestAbsoluteLink_NoPublicURLLeavesThePathAlone(t *testing.T) {
	// An instance with nothing configured must not start emitting links to
	// a guessed host. Degrading to today's relative path keeps the in-app
	// inbox working and is honest about what we do not know.
	if got := AbsoluteLink("", "/issues/CS-12"); got != "/issues/CS-12" {
		t.Errorf("AbsoluteLink with no base = %q, want the path unchanged", got)
	}
}

func TestAbsoluteLink_LeavesAnAlreadyAbsoluteURLAlone(t *testing.T) {
	// Not every link points into this app — a producer may carry a link to
	// the GitHub PR or Composio account a notification is about. Prefixing
	// the instance URL onto one of those would corrupt it.
	for _, raw := range []string{
		"https://github.com/crewship-ai/crewship/pull/1502",
		"http://other.test/x",
	} {
		if got := AbsoluteLink("https://crewship.example.com", raw); got != raw {
			t.Errorf("AbsoluteLink(%q) = %q, want it untouched", raw, got)
		}
	}
}

func TestAbsoluteLink_EmptyPathStaysEmpty(t *testing.T) {
	// Otherwise a producer that left a link blank would emit a bare link to
	// the dashboard root, which reads as "click here for nothing".
	if got := AbsoluteLink("https://crewship.example.com", ""); got != "" {
		t.Errorf("AbsoluteLink with an empty path = %q, want empty", got)
	}
}

func TestPrimaryLink_IsTheFirstOne(t *testing.T) {
	// Single-URL formats (the webhook payload's `url`, an e-mail footer)
	// have room for exactly one link, so link order is meaningful: the
	// first is the one a producer considers the primary action.
	msg := CategoryMessage{Links: []Link{
		{Label: "Open issue", Path: "/issues/CS-12"},
		{Label: "View journal", Path: "/journal?entry=je_1"},
	}}
	link, ok := msg.PrimaryLink()
	if !ok {
		t.Fatal("a message with links must report a primary one")
	}
	if link.Path != "/issues/CS-12" {
		t.Errorf("primary link = %q, want the first", link.Path)
	}
}

func TestPrimaryLink_NoneWhenThereAreNoLinks(t *testing.T) {
	if _, ok := (CategoryMessage{}).PrimaryLink(); ok {
		t.Error("a message with no links must not report a primary one")
	}
}
