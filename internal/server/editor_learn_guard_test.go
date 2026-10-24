package server

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

// A production server (listening on every interface) never learns, and so
// never probes, a loopback or metadata origin that a signed-in user's
// forged headers name; the running self-check sends them nothing.
func TestZeroConfigSelfCheckIgnoresInternalOriginsFromUsers(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.RequestURI)
		mu.Unlock()
	}))
	defer internal.Close()
	u, _ := url.Parse(internal.URL)

	cfg := config.Default() // listens on 0.0.0.0
	dir := t.TempDir()
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorBundleDir: dir, EditorBundles: []string{writeEditorBundle(t, dir)}}
	editor := newEditorOrigin(cfg, slog.Default())
	if editor.AllowsLoopbackProbes() {
		t.Fatal("a server on every interface allows loopback probes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	startEditorProxyCheck(ctx, &wg, editor, slog.Default(), newEditorProxyCheckClient(editor.AllowsLoopbackProbes()), []time.Duration{time.Millisecond, time.Hour})
	for _, host := range []string{u.Host, "169.254.169.254", "localhost:" + u.Port()} {
		for _, scheme := range []string{"http", "https"} {
			r := httptest.NewRequest("GET", "/api/v1/me", nil)
			r.Host = host
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Origin", scheme+"://"+host)
			editor.LearnOrigin(r, "mallory-"+scheme+host, func() bool { return true })
		}
	}
	time.Sleep(300 * time.Millisecond)
	cancel()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 0 || len(editor.CheckTargets()) != 0 {
		t.Fatalf("probed %q, targets %v", hits, editor.CheckTargets())
	}
}

// The self-check client refuses metadata, link-local and (unless allowed)
// loopback addresses at connect time, whatever name resolved to them.
func TestEditorSelfCheckClientRefusesInternalAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	for _, target := range []string{"http://169.254.169.254/", "http://0.0.0.0:1/", srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)} {
		_, err := newEditorProxyCheckClient(false).Get(target)
		if err == nil || !strings.Contains(err.Error(), "refuses to connect") {
			t.Errorf("%s: %v", target, err)
		}
	}
	if _, err := newEditorProxyCheckClient(true).Get("http://169.254.169.254/"); err == nil || !strings.Contains(err.Error(), "refuses to connect") {
		t.Errorf("metadata with loopback allowed: %v", err)
	}
	resp, err := newEditorProxyCheckClient(true).Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback allowed: %v", err)
	}
	resp.Body.Close()
}

// Studio documents whose frame-src names the request's origin stay out of
// shared caches through the real UI handler, which sets its own
// Cache-Control; their revalidation directives are kept.
func TestZeroConfigDocumentsPrivateThroughStaticHandler(t *testing.T) {
	cfg := config.Default()
	dir := t.TempDir()
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorBundleDir: dir, EditorBundles: []string{writeEditorBundle(t, dir)}}
	editor := newEditorOrigin(cfg, slog.Default())
	web := fstest.MapFS{
		"index.html":               {Data: []byte("<!doctype html>root")},
		"chat.html":                {Data: []byte("<!doctype html>chat")},
		"pages/_.html":             {Data: []byte("<!doctype html>page")},
		"_next/static/chunks/a.js": {Data: []byte("1")},
		"favicon.ico":              {Data: []byte("i")},
	}
	handler := editororigin.Gate(editor, securityHeadersMiddlewareWith(goapi.StaticFileHandler(web), editorHeaderOptions(cfg, editor)))
	get := func(p string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://crewship.example.test"+p, nil)
		r.TLS = &tls.ConnectionState{}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, p := range []string{"/", "/chat", "/index.html", "/pages/abc"} {
		w := get(p)
		cc := w.Header().Get("Cache-Control")
		if w.Code != 200 || !strings.HasPrefix(cc, "private") || !strings.Contains(cc, "no-cache") || strings.Contains(cc, "public") {
			t.Errorf("%s: HTTP %d Cache-Control %q", p, w.Code, cc)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "https://crewship.example.test/_editor/b/") {
			t.Errorf("%s: frame-src does not name the request origin", p)
		}
	}
	if cc := get("/_next/static/chunks/a.js").Header().Get("Cache-Control"); !strings.HasPrefix(cc, "public") {
		t.Errorf("hashed asset made private: %q", cc)
	}
}

func TestPrivateCacheControl(t *testing.T) {
	for in, want := range map[string]string{
		"":                                  "private",
		"no-cache, must-revalidate":         "private, no-cache, must-revalidate",
		"public, max-age=300":               "private, max-age=300",
		"no-store":                          "no-store",
		"private, max-age=60":               "private, max-age=60",
		"Public,max-age=31536000,immutable": "private, max-age=31536000, immutable",
	} {
		if got := privateCacheControl(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
