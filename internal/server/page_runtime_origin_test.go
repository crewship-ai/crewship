package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/editororigin"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// TestPageRuntimeOriginReachesTheHandler — a localhost Studio with no runtime
// origin configured must get the derived 127.0.0.1 origin all the way to the
// API (bootstrap served on that host) and the CSP (Studio may frame it), not
// only in config.PageRuntimeOrigin (#3108).
func TestPageRuntimeOriginReachesTheHandler(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		nextjs     string
		runtime    string // host the bootstrap must be served on; "" = none
	}{
		{name: "derived for localhost Studio", nextjs: "http://localhost:3001", runtime: "127.0.0.1:8080"},
		{name: "configured wins", nextjs: "http://localhost:3001", configured: "http://127.0.0.1:9999", runtime: "127.0.0.1:9999"},
		{name: "remote Studio derives nothing", nextjs: "https://studio.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.MigratedDB(t)
			cfg := config.Default()
			cfg.Auth.JWTSecret = "test-secret-for-page-runtime-origin-32"
			cfg.Auth.NextjsURL = tc.nextjs
			cfg.Server.Port = 8080
			cfg.Storage.PageRuntimeOrigin = tc.configured
			s := New(cfg, logging.New("error", "json", nil), &Deps{DB: db.DB})
			t.Cleanup(func() {
				if s.missionEngine != nil {
					s.missionEngine.Shutdown()
				}
				s.StopBackground()
			})
			h := s.httpServer.Handler

			for _, host := range []string{"127.0.0.1:8080", "127.0.0.1:9999"} {
				r := httptest.NewRequest("GET", "/api/v1/pages/runtime/bootstrap", nil)
				r.Host = host
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if want := host == tc.runtime; (w.Code == 200) != want {
					t.Fatalf("bootstrap on %s = %d, served want %v", host, w.Code, want)
				}
			}
			// Studio as the local browser reaches it.
			studio := httptest.NewRequest("GET", "/", nil)
			studio.Host = "localhost:8080"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, studio)
			csp := w.Header().Get("Content-Security-Policy")
			if tc.runtime != "" && !strings.Contains(csp, "http://"+tc.runtime) {
				t.Fatalf("CSP does not allow framing the runtime %s: %s", tc.runtime, csp)
			}
			if tc.runtime == "" && strings.Contains(csp, "127.0.0.1") {
				t.Fatalf("CSP frames a runtime nobody configured: %s", csp)
			}
			// A derived origin is framed only by a Studio reached on a
			// loopback host; a configured one by every Studio host.
			r := httptest.NewRequest("GET", "/", nil)
			r.Host = "crewship.example.com"
			w = httptest.NewRecorder()
			h.ServeHTTP(w, r)
			remote := w.Header().Get("Content-Security-Policy")
			if derived := tc.configured == "" && tc.runtime != ""; derived == strings.Contains(remote, "http://"+tc.runtime) && tc.runtime != "" {
				t.Fatalf("CSP for a remote Studio host (derived %v): %s", derived, remote)
			}
			// A reverse proxy that rewrites Host to localhost still serves
			// remote browsers; its forwarding headers say so.
			for _, header := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
				r = httptest.NewRequest("GET", "/", nil)
				r.Host = "localhost:8080"
				r.Header.Set(header, "for=203.0.113.7")
				w = httptest.NewRecorder()
				h.ServeHTTP(w, r)
				proxied := w.Header().Get("Content-Security-Policy")
				if derived := tc.configured == "" && tc.runtime != ""; derived == strings.Contains(proxied, "http://"+tc.runtime) && tc.runtime != "" {
					t.Fatalf("CSP behind a proxy (%s, derived %v): %s", header, derived, proxied)
				}
			}
		})
	}
}

// TestSameHostEditorRefusesTheDerivedRuntimeOrigin — the derived 127.0.0.1
// runtime origin hosts untrusted Page apps, so the same-host editor must
// treat it exactly like a configured runtime origin: refused there, served
// on the localhost Studio origin (#3108).
func TestSameHostEditorRefusesTheDerivedRuntimeOrigin(t *testing.T) {
	cfg := config.Default()
	if got := cfg.PageRuntimeOrigin(); got != "http://127.0.0.1:8080" {
		t.Fatalf("default config derives runtime %q, want http://127.0.0.1:8080", got)
	}
	dir := t.TempDir()
	digest := writeEditorBundle(t, dir)
	cfg.GridCraft = config.GridCraftConfig{Enabled: true, EditorEnabled: true, EditorBundleDir: dir, EditorBundles: []string{digest}}
	editor := newEditorOrigin(cfg, slog.Default())
	main := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	handler := editororigin.Gate(editor, securityHeadersMiddlewareWith(main, editorHeaderOptions(cfg, editor)))

	for host, want := range map[string]bool{"localhost:8080": true, "127.0.0.1:8080": false} {
		r := httptest.NewRequest("GET", "http://"+host+"/_editor/b/"+digest+"/index.html", nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Dest", "iframe")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if (w.Code == 200) != want {
			t.Errorf("editor on %s = %d, served want %v", host, w.Code, want)
		}
	}
}
