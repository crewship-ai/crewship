package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/hooks"
)

// While lifecycle hooks are paused every /api/v1/hooks route answers 503 with
// the reason, instead of acting; with them on, the handler runs.
func TestHooksPausedGate(t *testing.T) {
	ran := false
	h := hooksPausedGate(func(w http.ResponseWriter, _ *http.Request) { ran = true; w.WriteHeader(http.StatusOK) })

	hooks.SetEnabledForTest(t, false)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hooks", nil))
	if rec.Code != http.StatusServiceUnavailable || ran {
		t.Fatalf("paused: status %d, handler ran %v; want 503 and not run", rec.Code, ran)
	}
	if !strings.Contains(rec.Body.String(), "paused") {
		t.Fatalf("paused body %q does not say why", rec.Body.String())
	}

	hooks.SetEnabledForTest(t, true)
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hooks", nil))
	if rec.Code != http.StatusOK || !ran {
		t.Fatalf("enabled: status %d, handler ran %v; want 200 and run", rec.Code, ran)
	}
}
