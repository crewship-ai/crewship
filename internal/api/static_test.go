package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// fakeFS builds a minimal Next.js static export layout for the handler
// to walk. Files contain their own path so assertions can verify which
// file actually got served.
func fakeFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("ROOT")},
		"login.html":        {Data: []byte("LOGIN")},
		"crews.html":        {Data: []byte("CREWS")},
		"crews/agents.html": {Data: []byte("CREWS_AGENTS")},
		"chat/_.html":       {Data: []byte("CHAT_PLACEHOLDER")},
		"skills/_.html":     {Data: []byte("SKILLS_PLACEHOLDER")},
		"issues/_.html":     {Data: []byte("ISSUES_PLACEHOLDER")},
		// Older / directory-style placeholder for parity coverage:
		"old/_/index.html": {Data: []byte("OLD_DIR_PLACEHOLDER")},
		"icon.svg":         {Data: []byte("SVG")},
	}
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func TestStaticFileHandler_RootServesIndex(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	code, body := get(t, h, "/")
	if code != http.StatusOK || body != "ROOT" {
		t.Fatalf("/ → code=%d body=%q; want 200 ROOT", code, body)
	}
}

func TestStaticFileHandler_HtmlExtensionResolution(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /login → login.html
	code, body := get(t, h, "/login")
	if code != http.StatusOK || body != "LOGIN" {
		t.Fatalf("/login → code=%d body=%q; want 200 LOGIN", code, body)
	}
}

func TestStaticFileHandler_NestedExactPath(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /crews/agents → crews/agents.html
	code, body := get(t, h, "/crews/agents")
	if code != http.StatusOK || body != "CREWS_AGENTS" {
		t.Fatalf("/crews/agents → code=%d body=%q; want 200 CREWS_AGENTS", code, body)
	}
}

func TestStaticFileHandler_DynamicRoute_FlatPlaceholder(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /chat/filip → no chat/filip.html, no chat/filip/index.html →
	// dynamic-route lookup → chat/_.html
	code, body := get(t, h, "/chat/filip")
	if code != http.StatusOK || body != "CHAT_PLACEHOLDER" {
		t.Fatalf("/chat/filip → code=%d body=%q; want 200 CHAT_PLACEHOLDER", code, body)
	}
}

func TestStaticFileHandler_DynamicRoute_NestedPlaceholder(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /issues/CRE-78 → dynamic placeholder for top-level Issues route
	// (the IA refactor split off Plan/Run/Build/System pages from the
	// old /orchestration container).
	code, body := get(t, h, "/issues/CRE-78")
	if code != http.StatusOK || body != "ISSUES_PLACEHOLDER" {
		t.Fatalf("/issues/CRE-78 → code=%d body=%q; want 200 ISSUES_PLACEHOLDER", code, body)
	}
}

func TestStaticFileHandler_DynamicRoute_DirectoryStylePlaceholder(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /old/anything → falls through to old/_/index.html
	code, body := get(t, h, "/old/anything")
	if code != http.StatusOK || body != "OLD_DIR_PLACEHOLDER" {
		t.Fatalf("/old/anything → code=%d body=%q; want 200 OLD_DIR_PLACEHOLDER", code, body)
	}
}

func TestStaticFileHandler_DynamicRoute_SkillsPlaceholder(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /skills/some-uuid → skills/_.html
	code, body := get(t, h, "/skills/some-uuid-123")
	if code != http.StatusOK || body != "SKILLS_PLACEHOLDER" {
		t.Fatalf("/skills/some-uuid-123 → code=%d body=%q; want 200 SKILLS_PLACEHOLDER", code, body)
	}
}

func TestStaticFileHandler_UnknownDynamicRoute_FallsBackToRoot(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /unknown/path with no matching placeholder anywhere → SPA fallback
	// serves root index.html so the client router can render an empty
	// state, NOT a 404.
	code, body := get(t, h, "/totally/random/url")
	if code != http.StatusOK || body != "ROOT" {
		t.Fatalf("/totally/random/url → code=%d body=%q; want 200 ROOT", code, body)
	}
}

func TestStaticFileHandler_ExactFileWithExtension(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	// /icon.svg → exact match, served verbatim (no .html resolution)
	code, body := get(t, h, "/icon.svg")
	if code != http.StatusOK || body != "SVG" {
		t.Fatalf("/icon.svg → code=%d body=%q; want 200 SVG", code, body)
	}
}

// nextStaticFS adds the _next/static/ tree shape to fakeFS so the M10
// dir-autoindex regression has something to walk. The chunk filename
// uses Next.js's canonical hashed shape so the assertion mirrors a real
// production build.
func nextStaticFS() fstest.MapFS {
	fs := fakeFS()
	fs["_next/static/chunks/main-abc123.js"] = &fstest.MapFile{Data: []byte("CHUNK_JS")}
	fs["_next/static/css/styles-def456.css"] = &fstest.MapFile{Data: []byte("CHUNK_CSS")}
	return fs
}

// TestStaticFileHandler_NextDirListingBlocked pins audit M10: requests
// to a /_next/* directory must NOT return http.FileServer's default
// HTML autoindex (which enumerates build IDs + chunk filenames).
// Trailing-slash form is the canonical signal; the no-slash variant
// is caught by the pre-Stat IsDir check before FileServer's redirect
// runs.
func TestStaticFileHandler_NextDirListingBlocked(t *testing.T) {
	h := StaticFileHandler(nextStaticFS())

	for _, path := range []string{
		"/_next/static/",
		"/_next/static/chunks/",
		"/_next/static/chunks",
		"/_next/",
	} {
		code, _ := get(t, h, path)
		if code != http.StatusNotFound {
			t.Errorf("%s → code=%d, want 404 (no autoindex)", path, code)
		}
	}

	// Verifies the regression boundary: a real chunk file in the
	// same tree still serves -- this is the legitimate cache-hit
	// path that the autoindex defence must not break.
	code, body := get(t, h, "/_next/static/chunks/main-abc123.js")
	if code != http.StatusOK || body != "CHUNK_JS" {
		t.Errorf("real chunk file: code=%d body=%q; want 200 CHUNK_JS", code, body)
	}
}

// Regression: the bug that prompted the dynamic-route lookup was that
// /chat/filip was falling through to root index.html and rendering the
// dashboard instead of the chat page. Pin that explicitly so a future
// change to the priority order doesn't reintroduce it.
func TestStaticFileHandler_ChatSlugDoesNotFallBackToDashboard(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	code, body := get(t, h, "/chat/filip")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	if body == "ROOT" {
		t.Fatal("/chat/filip should NOT serve root index.html — that's the bug we just fixed (dashboard rendered instead of chat)")
	}
}

// Audit 2026-05-23 (ffuf finding): every dotfile / VCS / backup path was
// resolving to HTTP 200 SPA fallback, drowning real endpoints in noise
// and giving the illusion that paths like /.htpasswd "exist". 404 those.
func TestStaticFileHandler_SensitivePathsReturn404(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	cases := []string{
		"/.htpasswd",
		"/.htaccess",
		"/.git/config",
		"/.git",
		"/.gitignore",
		"/.env",
		"/.env.local",
		"/.ssh/id_rsa",
		"/.bash_history",
		"/.aws/credentials",
		"/.DS_Store",
		"/.svn/entries",
		"/path/.git/HEAD",
		"/index.html.bak",
		"/config.old",
		"/foo~",
		"/config.tmp",
		"/data.backup",
		"/notes.sav",
		// Regression: .well-known exempts only its own segment, so a
		// nested sensitive segment still 404s (CodeRabbit PR #551).
		"/.well-known/.git/config",
		"/.well-known/.env",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			code, _ := get(t, h, p)
			if code != http.StatusNotFound {
				t.Errorf("expected 404, got %d", code)
			}
		})
	}
}

// .well-known/* (RFC 8615) is the documented exception — must still
// reach the SPA fallback so e.g. acme-challenge / security.txt can be
// served from the static export.
func TestStaticFileHandler_WellKnownNotBlocked(t *testing.T) {
	h := StaticFileHandler(fakeFS())
	code, _ := get(t, h, "/.well-known/security.txt")
	if code == http.StatusNotFound {
		// .well-known/security.txt is not present in fakeFS, so SPA
		// fallback serves index.html (200, body=ROOT). The point is
		// we must NOT have been short-circuited to 404 by the
		// dotfile guard.
		t.Fatal(".well-known/* must not be blocked by sensitive-path guard")
	}
}
