package server

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

func directiveOf(policy, name string) string {
	for _, d := range strings.Split(policy, ";") {
		if d = strings.TrimSpace(d); d == name || strings.HasPrefix(d, name+" ") {
			return d
		}
	}
	return ""
}

// Zero-config: after install nothing names the public Studio URL (packaging
// sets neither CREWSHIP_PAGE_STUDIO_ORIGIN nor CREWSHIP_NEXTJS_URL). The
// editor's policy and Studio's frame-src must then follow the origin each
// browser request reached, never the internal default (localhost).
func TestZeroConfigEditorFollowsTheRequestOrigin(t *testing.T) {
	cfg := config.Default()
	if cfg.Storage.PageStudioOrigin != "" {
		t.Fatalf("default config names a public Studio origin: %q", cfg.Storage.PageStudioOrigin)
	}
	dir := t.TempDir()
	digest := writeEditorBundle(t, dir)
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorBundleDir: dir, EditorBundles: []string{digest}}
	editor := newEditorOrigin(cfg, slog.Default())
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	handler := editororigin.Gate(editor, securityHeadersMiddlewareWith(main, editorHeaderOptions(cfg, editor)))

	for _, host := range []string{"crewship.example.test", "studio.other.example:8443"} {
		studio := "https://" + host
		r := httptest.NewRequest("GET", studio+"/chat", nil)
		r.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if got, want := frameSrcOf(w.Header().Get("Content-Security-Policy")), "frame-src "+studio+"/_editor/b/"; got != want {
			t.Errorf("Studio on %s: %q, want %q", host, got, want)
		}

		r = httptest.NewRequest("GET", studio+"/_editor/b/"+digest+"/index.html", nil)
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		policy := w.Header().Get("Content-Security-Policy")
		if w.Code != 200 || directiveOf(policy, "frame-ancestors") != "frame-ancestors "+studio || directiveOf(policy, "connect-src") != "connect-src "+studio+"/_editor/b/" {
			t.Errorf("editor on %s: %d %s", host, w.Code, policy)
		}
		if strings.Contains(policy, "localhost") {
			t.Errorf("editor on %s names localhost: %s", host, policy)
		}
	}
}

// The scheme follows Crewship's trusted-proxy rule (X-Forwarded-Proto only
// from CREWSHIP_TRUSTED_PROXY_CIDRS, loopback by default). Untrusted, it is
// http, which a CSP source also matches for https, so a proxy Crewship does
// not trust still gets a working policy and never a forged one. Studio
// documents whose policy names the request's origin stay out of shared
// caches; hashed static assets do not.
func TestZeroConfigSchemeAndCaching(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorBundleDir: dir, EditorBundles: []string{writeEditorBundle(t, dir)}}
	editor := newEditorOrigin(cfg, slog.Default())
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	handler := editororigin.Gate(editor, securityHeadersMiddlewareWith(main, editorHeaderOptions(cfg, editor)))
	for _, tc := range []struct {
		remote, proto, want string
	}{
		{"127.0.0.1:5000", "https", "https://crewship.example.test/_editor/b/"},
		{"192.0.2.1:5000", "https", "http://crewship.example.test/_editor/b/"},
		{"127.0.0.1:5000", "", "http://crewship.example.test/_editor/b/"},
	} {
		r := httptest.NewRequest("GET", "http://crewship.example.test/chat", nil)
		r.RemoteAddr = tc.remote
		if tc.proto != "" {
			r.Header.Set("X-Forwarded-Proto", tc.proto)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if got := frameSrcOf(w.Header().Get("Content-Security-Policy")); got != "frame-src "+tc.want {
			t.Errorf("from %s proto %q: %q, want %q", tc.remote, tc.proto, got, tc.want)
		}
		if got := w.Header().Get("Cache-Control"); got != "private" {
			t.Errorf("UI document Cache-Control %q", got)
		}
	}
	for _, p := range []string{"/_next/static/chunks/app.js", "/favicon.ico", "/api/v1/auth/session"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://crewship.example.test"+p, nil))
		if w.Header().Get("Cache-Control") == "private" {
			t.Errorf("%s made private", p)
		}
	}
	// A Host no browser sends gets no editor source at all.
	r := httptest.NewRequest("GET", "/chat", nil)
	r.Host = "evil.test@crewship.example.test"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if strings.Contains(w.Header().Get("Content-Security-Policy"), "_editor") {
		t.Fatalf("invalid Host framed the editor: %s", w.Header().Get("Content-Security-Policy"))
	}
}

// A configured public Studio origin is the only one the editor answers on:
// a request that reaches Crewship under another Host (a proxy that rewrites
// it) is refused, and Studio frames nothing there.
func TestConfiguredStudioOriginFailsClosedOnOtherHosts(t *testing.T) {
	cfg, editor, digest := sameHostEditor(t, "https://crewship.example.test")
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	handler := editororigin.Gate(editor, securityHeadersMiddlewareWith(main, editorHeaderOptions(cfg, editor)))
	for host, want := range map[string]int{"crewship.example.test": 200, "127.0.0.1:8080": http.StatusMisdirectedRequest, "other.example.test": http.StatusMisdirectedRequest} {
		r := httptest.NewRequest("GET", "/_editor/b/"+digest+"/index.html", nil)
		r.Host = host
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s: %d, want %d", host, w.Code, want)
		}
		r = httptest.NewRequest("GET", "/chat", nil)
		r.Host = host
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		framed := strings.Contains(w.Header().Get("Content-Security-Policy"), "https://crewship.example.test/_editor/b/")
		if framed != (want == 200) {
			t.Errorf("%s: Studio frame-src %q", host, frameSrcOf(w.Header().Get("Content-Security-Policy")))
		}
	}
}
