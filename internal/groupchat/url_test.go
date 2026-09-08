package groupchat

import (
	"net/url"
	"testing"
)

func TestURLKeepsConversationInOneQueryParameter(t *testing.T) {
	for _, id := range []string{"conversation-1", "id&workspace_id=other", "//outside.example/path?x=1", "Žluťoučký kůň"} {
		u, err := url.Parse(URL(id))
		if err != nil {
			t.Fatal(err)
		}
		if u.Path != "/chat" || u.Host != "" || u.Scheme != "" || len(u.Query()) != 1 || u.Query().Get("conversation") != id {
			t.Fatalf("unsafe URL %q for %q", URL(id), id)
		}
	}
}
