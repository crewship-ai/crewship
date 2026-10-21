package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
)

func TestSlidesHumanServerRegistration(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Auth.JWTSecret = "slides-registration-test-secret-32chars"
			cfg.Storage.BasePath = t.TempDir()
			cfg.Storage.LogPath = t.TempDir()
			cfg.Storage.MemoryRoot = t.TempDir()
			cfg.Slides.Enabled = enabled
			s := New(cfg, logging.New("error", "json", nil), &Deps{DB: openTestDB(t)})
			t.Cleanup(s.StopBackground)
			for _, route := range []struct{ method, path string }{
				{"GET", "/api/v1/pages/hidden/slides"},
				{"GET", "/api/v1/pages/hidden/slides/revisions/1"},
				{"POST", "/api/v1/pages/hidden/slides/initialize"},
			} {
				response := httptest.NewRecorder()
				s.mux.ServeHTTP(response, httptest.NewRequest(route.method, route.path, nil))
				want := http.StatusNotFound
				if enabled {
					want = http.StatusUnauthorized
				}
				if response.Code != want {
					t.Errorf("%s %s: HTTP %d, want %d", route.method, route.path, response.Code, want)
				}
			}
		})
	}
}
