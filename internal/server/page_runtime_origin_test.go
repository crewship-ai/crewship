package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
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
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			csp := w.Header().Get("Content-Security-Policy")
			if tc.runtime != "" && !strings.Contains(csp, "http://"+tc.runtime) {
				t.Fatalf("CSP does not allow framing the runtime %s: %s", tc.runtime, csp)
			}
			if tc.runtime == "" && strings.Contains(csp, "127.0.0.1") {
				t.Fatalf("CSP frames a runtime nobody configured: %s", csp)
			}
		})
	}
}
