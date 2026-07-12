package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestVersionHeaderMiddleware locks the version-skew contract: every API
// response carries X-Crewship-Server-Version so the CLI can detect a stale
// client without an extra round-trip (a stale CLI against a newer server is
// the single most common source of confusing API errors).
func TestVersionHeaderMiddleware(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	t.Run("sets header", func(t *testing.T) {
		h := VersionHeader("1.2.3", inner)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/agents", nil))
		if got := rec.Header().Get("X-Crewship-Server-Version"); got != "1.2.3" {
			t.Errorf("X-Crewship-Server-Version = %q, want 1.2.3", got)
		}
		if rec.Code != http.StatusTeapot {
			t.Errorf("inner handler not invoked, code %d", rec.Code)
		}
	})

	t.Run("empty version omits header", func(t *testing.T) {
		h := VersionHeader("", inner)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/agents", nil))
		if got := rec.Header().Get("X-Crewship-Server-Version"); got != "" {
			t.Errorf("header should be absent for empty version, got %q", got)
		}
	})
}
