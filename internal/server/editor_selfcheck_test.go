package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

// sameHostEditor builds the same-host editor the way server.New does. A
// non-empty studio is the operator-configured public Studio origin
// (CREWSHIP_PAGE_STUDIO_ORIGIN); empty is the zero-config default.
func sameHostEditor(t *testing.T, studio string) (*config.Config, *editororigin.Handler, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Storage.PageStudioOrigin = studio
	// The stacks below serve on a loopback port, as a development server
	// bound to loopback does; only then are loopback origins learned.
	cfg.Server.Host = "127.0.0.1"
	dir := t.TempDir()
	digest := writeEditorBundle(t, dir)
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorBundleDir: dir, EditorBundles: []string{digest}}
	editor := newEditorOrigin(cfg, slog.Default())
	if editor == nil {
		t.Fatal("same-host editor not built")
	}
	return cfg, editor, digest
}

// The editor shares the Studio host in same-host mode. Every editor response
// must carry exactly Studio's HSTS, or loading the editor would replace the
// host's stored policy with a weaker one (RFC 6797 section 8.1).
func TestSameHostEditorKeepsStudioHSTS(t *testing.T) {
	cfg, editor, digest := sameHostEditor(t, "")
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	handler := editororigin.Gate(editor, securityHeadersMiddlewareWith(main, editorHeaderOptions(cfg, editor)))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/chat", nil))
	studioHSTS := w.Header().Get("Strict-Transport-Security")
	if studioHSTS == "" {
		t.Fatal("Studio sends no HSTS")
	}
	for _, p := range []string{"/_editor/b/" + digest + "/index.html", "/_editor/b/x", "/_EDITOR/"} {
		r := httptest.NewRequest("GET", p, nil)
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if got := w.Header().Values("Strict-Transport-Security"); len(got) != 1 || got[0] != studioHSTS {
			t.Errorf("%s: HSTS %q, Studio sends %q", p, got, studioHSTS)
		}
	}
}

// selfCheckStack is a Studio origin as the real server builds it (Gate,
// security headers, the editor), behind a front layer that can be swapped,
// served on a loopback port.
type selfCheckStack struct {
	srv    *httptest.Server
	mu     sync.Mutex
	front  func(http.Handler) http.Handler
	gated  http.Handler
	editor *editororigin.Handler
	digest string
}

func newSelfCheckStack(t *testing.T, configured bool) *selfCheckStack {
	t.Helper()
	s := &selfCheckStack{front: func(h http.Handler) http.Handler { return h }}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		h := s.front(s.gated)
		s.mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	studio := ""
	if configured {
		studio = s.srv.URL
	}
	cfg, editor, digest := sameHostEditor(t, studio)
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	s.gated = editororigin.Gate(editor, securityHeadersMiddlewareWith(main, editorHeaderOptions(cfg, editor)))
	s.editor, s.digest = editor, digest
	return s
}

func (s *selfCheckStack) setFront(f func(http.Handler) http.Handler) {
	s.mu.Lock()
	s.front = f
	s.mu.Unlock()
}

func ssoGate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_editor/") {
			http.Redirect(w, r, "https://sso.corp.example/login", http.StatusFound)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fastSelfCheck(t *testing.T) {
	t.Helper()
	confirm, passed, failed := editorProxyCheckConfirm, editorProxyCheckPassedEvery, editorProxyCheckFailedEvery
	editorProxyCheckConfirm, editorProxyCheckPassedEvery, editorProxyCheckFailedEvery = 0, 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		editorProxyCheckConfirm, editorProxyCheckPassedEvery, editorProxyCheckFailedEvery = confirm, passed, failed
	})
}

func healthzEditor(t *testing.T, editor *editororigin.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	(&Server{editor: editor}).handleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	var body struct {
		Editor string `json:"workbook_editor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz: %v %s", err, rec.Body.String())
	}
	return body.Editor
}

func editorDocument(s *selfCheckStack, host string) int {
	r := httptest.NewRequest("GET", "/_editor/b/"+s.digest+"/index.html", nil)
	r.Host = host
	r.Header.Set("Sec-Fetch-Dest", "iframe")
	w := httptest.NewRecorder()
	s.gated.ServeHTTP(w, r)
	return w.Code
}

// With a configured Studio origin the server checks it through the public
// URL and disables the editor when a proxy defeats the path-scoped
// isolation. /healthz reports the outcome as a coarse state only.
func TestSameHostEditorProxySelfCheck(t *testing.T) {
	fastSelfCheck(t)
	for _, tc := range []struct {
		name     string
		front    func(http.Handler) http.Handler
		health   string
		document int
	}{
		{"direct", func(h http.Handler) http.Handler { return h }, editororigin.HealthOK, 200},
		{"sso gate redirects the prefix", ssoGate, editororigin.HealthDisabled, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSelfCheckStack(t, true)
			s.setFront(tc.front)
			if got := healthzEditor(t, s.editor); got != editororigin.HealthPending {
				t.Fatalf("before any check: %q", got)
			}
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			startEditorProxyCheck(ctx, &wg, s.editor, slog.Default(), s.srv.Client(), []time.Duration{0})
			waitFor(t, "a verdict", func() bool { return s.editor.Health() != editororigin.HealthPending })
			cancel()
			wg.Wait()
			if got := healthzEditor(t, s.editor); got != tc.health {
				t.Fatalf("healthz %q, want %q (%+v)", got, tc.health, s.editor.Detail())
			}
			if got := editorDocument(s, strings.TrimPrefix(s.srv.URL, "http://")); got != tc.document {
				t.Fatalf("editor document %d, want %d", got, tc.document)
			}
		})
	}
	// Without an enabled editor /healthz is unchanged.
	rec := httptest.NewRecorder()
	(&Server{}).handleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	if strings.Contains(rec.Body.String(), "workbook_editor") {
		t.Fatalf("healthz reports an editor that is off: %s", rec.Body.String())
	}
}

// Zero-config: nothing is probed (and nothing reported as passed) until a
// signed-in browser request shows the public origin; then that origin is
// probed promptly, not the internal default.
func TestZeroConfigSelfCheckProbesTheLearnedOrigin(t *testing.T) {
	fastSelfCheck(t)
	s := newSelfCheckStack(t, false)
	var mu sync.Mutex
	var probedHosts []string
	s.setFront(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			probedHosts = append(probedHosts, r.Host)
			mu.Unlock()
			h.ServeHTTP(w, r)
		})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	startEditorProxyCheck(ctx, &wg, s.editor, slog.Default(), s.srv.Client(), []time.Duration{time.Millisecond, time.Hour})
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n := len(probedHosts)
	mu.Unlock()
	if n != 0 || healthzEditor(t, s.editor) != editororigin.HealthPending {
		t.Fatalf("probed %d times or reported %q before any browser used Studio", n, healthzEditor(t, s.editor))
	}
	// A signed-in Studio page as the auth middleware hands it over.
	host := strings.TrimPrefix(s.srv.URL, "http://")
	r := httptest.NewRequest("GET", "/api/v1/me/preferences", nil)
	r.Host = host
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Referer", s.srv.URL+"/chat")
	s.editor.LearnOrigin(r, "admin", func() bool { return true })
	waitFor(t, "the learned origin to pass", func() bool { return s.editor.Health() == editororigin.HealthOK })
	cancel()
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	for _, h := range probedHosts {
		if h != host {
			t.Fatalf("probed %q, not the learned origin %q", h, host)
		}
	}
}

// One transient redirect (a maintenance page) does not disable the editor;
// a repeated one does, and the editor comes back by itself once the check
// passes editororigin.ReenableAfterPasses times in a row.
func TestSameHostEditorSelfCheckDisablesAndRecovers(t *testing.T) {
	fastSelfCheck(t)
	s := newSelfCheckStack(t, true)
	host := strings.TrimPrefix(s.srv.URL, "http://")
	var mu sync.Mutex
	redirects := 1
	s.setFront(func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			redirect := redirects > 0 && strings.HasPrefix(r.URL.Path, "/_editor/")
			if redirect {
				redirects--
			}
			mu.Unlock()
			if redirect {
				http.Redirect(w, r, "/maintenance", http.StatusFound)
				return
			}
			h.ServeHTTP(w, r)
		})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	startEditorProxyCheck(ctx, &wg, s.editor, slog.Default(), s.srv.Client(), []time.Duration{0})
	waitFor(t, "first verdict", func() bool { return s.editor.Health() != editororigin.HealthPending })
	if got := s.editor.Health(); got != editororigin.HealthOK {
		t.Fatalf("a single transient redirect changed the state: %s %+v", got, s.editor.Detail())
	}
	// A maintenance window: every probe redirected for a while.
	mu.Lock()
	redirects = 1 << 30
	mu.Unlock()
	waitFor(t, "the editor to be disabled", func() bool { return s.editor.Health() == editororigin.HealthDisabled })
	if editorDocument(s, host) != 503 {
		t.Fatal("disabled editor still served")
	}
	mu.Lock()
	redirects = 0
	mu.Unlock()
	waitFor(t, "the editor to come back", func() bool { return s.editor.Health() == editororigin.HealthOK })
	cancel()
	wg.Wait()
	if editorDocument(s, host) != 200 {
		t.Fatal("re-enabled editor not served")
	}
}
