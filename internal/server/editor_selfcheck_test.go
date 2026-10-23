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

// sameHostEditor builds the zero-config editor the way server.New does.
func sameHostEditor(t *testing.T, studio string) (*config.Config, *editororigin.Handler, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Auth.NextjsURL = studio
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
	cfg, editor, digest := sameHostEditor(t, "https://studio.example.test")
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

// The server checks its own public URL for a proxy that redirects or
// normalises /_editor/ and disables the editor when one would defeat the
// path-scoped isolation. /healthz reports the outcome for `crewship doctor`.
func TestSameHostEditorProxySelfCheck(t *testing.T) {
	for _, tc := range []struct {
		name     string
		front    func(http.Handler) http.Handler
		check    string
		disabled bool
	}{
		{"direct", func(h http.Handler) http.Handler { return h }, editororigin.ProxyCheckPassed, false},
		{"sso gate redirects the prefix", func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/_editor/") {
					http.Redirect(w, r, "/login", http.StatusFound)
					return
				}
				h.ServeHTTP(w, r)
			})
		}, editororigin.ProxyCheckFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var gated http.Handler
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				h := gated
				mu.Unlock()
				h.ServeHTTP(w, r)
			}))
			defer srv.Close()
			cfg, editor, digest := sameHostEditor(t, srv.URL)
			main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
			mu.Lock()
			gated = tc.front(editororigin.Gate(editor, securityHeadersMiddlewareWith(main, editorHeaderOptions(cfg, editor))))
			mu.Unlock()

			defer func(d time.Duration) { editorProxyCheckConfirm = d }(editorProxyCheckConfirm)
			editorProxyCheckConfirm = 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var wg sync.WaitGroup
			startEditorProxyCheck(ctx, &wg, editor, cfg.PageStudioOrigin(), slog.Default(), srv.Client(), []time.Duration{0})
			deadline := time.Now().Add(10 * time.Second)
			for editor.Status().ProxyCheck == editororigin.ProxyCheckPending && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			cancel()
			wg.Wait()
			if s := editor.Status(); s.ProxyCheck != tc.check || s.Disabled != tc.disabled {
				t.Fatalf("status %+v, want check %q disabled %v", s, tc.check, tc.disabled)
			}
			r := httptest.NewRequest("GET", "/_editor/b/"+digest+"/index.html", nil)
			r.Header.Set("Sec-Fetch-Dest", "iframe")
			w := httptest.NewRecorder()
			editororigin.Gate(editor, main).ServeHTTP(w, r)
			if want := map[bool]int{false: 200, true: 503}[tc.disabled]; w.Code != want {
				t.Fatalf("editor document %d, want %d", w.Code, want)
			}

			s := &Server{editor: editor}
			rec := httptest.NewRecorder()
			s.handleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
			var body struct {
				Editor *editororigin.Status `json:"workbook_editor"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Editor == nil {
				t.Fatalf("healthz without workbook_editor: %v %s", err, rec.Body.String())
			}
			if body.Editor.Mode != "same-host" || body.Editor.ProxyCheck != tc.check || body.Editor.Disabled != tc.disabled || body.Editor.URL != srv.URL+"/_editor/b/" {
				t.Fatalf("healthz editor %+v", body.Editor)
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

// One transient redirect (a maintenance page) does not disable the editor;
// a repeated one does.
func TestSameHostEditorProxySelfCheckConfirmsBeforeDisabling(t *testing.T) {
	defer func(d time.Duration) { editorProxyCheckConfirm = d }(editorProxyCheckConfirm)
	editorProxyCheckConfirm = 0
	var mu sync.Mutex
	var gated http.Handler
	redirects := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		h := gated
		if redirects > 0 && strings.HasPrefix(r.URL.Path, "/_editor/") {
			redirects--
			mu.Unlock()
			http.Redirect(w, r, "/maintenance", http.StatusFound)
			return
		}
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	_, editor, _ := sameHostEditor(t, srv.URL)
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mu.Lock()
	gated = editororigin.Gate(editor, main)
	redirects = 1
	mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	startEditorProxyCheck(ctx, &wg, editor, srv.URL, slog.Default(), srv.Client(), []time.Duration{0, time.Hour})
	deadline := time.Now().Add(10 * time.Second)
	for editor.Status().ProxyCheck == editororigin.ProxyCheckPending && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	wg.Wait()
	if s := editor.Status(); s.ProxyCheck != editororigin.ProxyCheckPassed || s.Disabled {
		t.Fatalf("a single transient redirect disabled the editor: %+v", s)
	}
}
