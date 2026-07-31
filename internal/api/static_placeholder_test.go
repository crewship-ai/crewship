package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// unbuiltFS is what web.FS() yields on a binary compiled from a checkout
// where `pnpm build` + the embed sync never ran: web/out/ holds only the
// tracked placeholder that keeps `//go:embed all:out` resolvable (#1567).
func unbuiltFS() fstest.MapFS {
	return fstest.MapFS{
		webPlaceholderFile: {Data: []byte("<h1>The Crewship web UI was not built into this binary.</h1>")},
	}
}

func TestStaticFileHandler_PlaceholderOnly_Serves503(t *testing.T) {
	h := StaticFileHandler(unbuiltFS())

	// Every route, not just "/". A SPA's deep links are what people
	// actually have bookmarked, and a 200 on any of them would keep the
	// missing build looking like a frontend bug.
	for _, path := range []string{"/", "/login", "/crews", "/chat/filip", "/icon.svg"} {
		code, body := get(t, h, path)
		if code != http.StatusServiceUnavailable {
			t.Errorf("GET %s = %d, want 503", path, code)
		}
		if !strings.Contains(body, "web UI was not built") {
			t.Errorf("GET %s body = %q, want the placeholder explanation", path, body)
		}
	}
}

func TestStaticFileHandler_PlaceholderOnly_SetsBuildHeader(t *testing.T) {
	h := StaticFileHandler(unbuiltFS())

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Crewship-Web-Build"); got != "placeholder" {
		t.Errorf("X-Crewship-Web-Build = %q, want %q", got, "placeholder")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — a cached 503 outlives the rebuild", got)
	}
}

// A real export must never trip the degraded path, and must never leak the
// placeholder header — that header is what a deploy check keys on.
func TestStaticFileHandler_RealExport_NotPlaceholderMode(t *testing.T) {
	fs := fakeFS()
	fs[webPlaceholderFile] = &fstest.MapFile{Data: []byte("PLACEHOLDER")}

	h := StaticFileHandler(fs)

	code, body := get(t, h, "/")
	if code != http.StatusOK || body != "ROOT" {
		t.Fatalf("GET / = %d %q, want 200 ROOT", code, body)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Crewship-Web-Build"); got != "" {
		t.Errorf("X-Crewship-Web-Build = %q on a real export, want empty", got)
	}
}

// No index.html AND no placeholder (e.g. web/out/ was wiped and refilled
// with a partial artifact) keeps the pre-existing behaviour rather than
// panicking on the missing placeholder read.
func TestStaticFileHandler_NoIndexNoPlaceholder_Falls404(t *testing.T) {
	h := StaticFileHandler(fstest.MapFS{"icon.svg": {Data: []byte("SVG")}})

	if code, _ := get(t, h, "/"); code != http.StatusNotFound {
		t.Errorf("GET / = %d, want 404", code)
	}
	if code, body := get(t, h, "/icon.svg"); code != http.StatusOK || body != "SVG" {
		t.Errorf("GET /icon.svg = %d %q, want 200 SVG", code, body)
	}
}
