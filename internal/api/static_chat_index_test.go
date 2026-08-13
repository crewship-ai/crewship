package api

import (
	"net/http"
	"testing"
	"testing/fstest"
)

// The /chat index is the structurally risky half of "chat is a primary
// surface": everything else is a component, this is a route that has to
// survive `output: "export"` and this handler.
//
// Two files land in the export for the chat subtree and they look alike:
//
//	out/chat.html    the index page  (app/(dashboard)/chat/page.tsx)
//	out/chat/_.html  the per-agent placeholder for [agentSlug]
//
// The handler's ordering is what keeps them apart. `path + ".html"` is tried
// before the dynamic-placeholder rewrite, so /chat lands on the index and
// /chat/<slug> falls through to the placeholder. Neither needs an entry in
// static.go — but nothing asserted it, and the failure mode if the order ever
// flips is silent: /chat would hydrate the per-agent shell, which reads the
// slug off window.location.pathname, finds none, and renders "Could not read
// agent slug from URL" instead of the index.
//
// Before app/(dashboard)/chat/page.tsx existed there was no chat.html at all
// and /chat fell all the way through to the SPA index — the 404 that
// hooks/use-active-runs.ts routed around by sending agent runs to /crews.
//
// Not asserted here, because it is not a chat behaviour: a TRAILING slash
// ("/chat/") still lands on the SPA index. `path + ".html"` becomes
// "chat/.html", the exact stat hits a directory, and the one-level rewrite
// needs two segments. Every route in the export behaves that way ("/crews/",
// "/issues/"), nothing in the app links with a trailing slash, and narrowing
// it for chat alone would leave the inconsistency somewhere less visible.
func chatExportFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":   {Data: []byte("ROOT")},
		"chat.html":    {Data: []byte("CHAT_INDEX")},
		"chat.txt":     {Data: []byte("CHAT_INDEX_RSC")},
		"chat/_.html":  {Data: []byte("CHAT_PLACEHOLDER")},
		"chat/_/x.txt": {Data: []byte("NEXT_MANIFEST")},
	}
}

func TestStaticFileHandler_ChatIndex(t *testing.T) {
	h := StaticFileHandler(chatExportFS())

	cases := []struct {
		name string
		path string
		want string
	}{
		{"bare index", "/chat", "CHAT_INDEX"},
		{"explicit html", "/chat.html", "CHAT_INDEX"},
		// The session is a query parameter, never a path segment — a deeper
		// route is exactly what the one-level rewrite cannot serve.
		{"index with a query string", "/chat?session=abc", "CHAT_INDEX"},
		// The sibling dynamic route must be unaffected by the index existing.
		{"per-agent placeholder", "/chat/ada", "CHAT_PLACEHOLDER"},
		{"per-agent with session", "/chat/ada?session=abc", "CHAT_PLACEHOLDER"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := get(t, h, tc.path)
			if code != http.StatusOK || body != tc.want {
				t.Fatalf("%s → code=%d body=%q; want 200 %s", tc.path, code, body, tc.want)
			}
		})
	}
}

// A directory named `chat/` sits next to `chat.html`. http.FileServer would
// redirect /chat → /chat/ and then autoindex it; the handler must not, because
// the export puts no index.html inside chat/.
func TestStaticFileHandler_ChatIndexNotADirectoryListing(t *testing.T) {
	h := StaticFileHandler(chatExportFS())
	code, body := get(t, h, "/chat")
	if code != http.StatusOK {
		t.Fatalf("/chat → code=%d; want 200", code)
	}
	if body == "ROOT" {
		t.Fatal("/chat fell through to the SPA index — the export's chat.html was not served")
	}
	if body != "CHAT_INDEX" {
		t.Fatalf("/chat → body=%q; want CHAT_INDEX", body)
	}
}

// Without a chat.html in the export — i.e. before the index page existed —
// /chat is the SPA fallback. Pinned so the meaning of the test above is
// unambiguous: it is the export that makes /chat resolve, not the handler.
func TestStaticFileHandler_ChatIndexRequiresTheExportedPage(t *testing.T) {
	withoutIndex := fstest.MapFS{
		"index.html":  {Data: []byte("ROOT")},
		"chat/_.html": {Data: []byte("CHAT_PLACEHOLDER")},
	}
	code, body := get(t, StaticFileHandler(withoutIndex), "/chat")
	if code != http.StatusOK || body != "ROOT" {
		t.Fatalf("/chat with no chat.html → code=%d body=%q; want 200 ROOT", code, body)
	}
}
