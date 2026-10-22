package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// Normal builds never advertise a test barrier, even with its environment set.
func TestHealthzOmitsTestBarrierWhenInactive(t *testing.T) {
	t.Setenv(appengine.TestBarrierDirEnv, t.TempDir())
	t.Setenv(appengine.TestBarrierSlotEnv, "dev2")
	s := newTestServer()
	if err := startGridCraftTestBarrier(nil); err != nil {
		t.Fatal(err)
	}
	if appengine.TestBarrierActive() {
		t.Skip("tagged build: covered by the tagged health test")
	}
	recorder := httptest.NewRecorder()
	s.mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))
	if strings.Contains(recorder.Body.String(), "test_barrier") {
		t.Fatalf("inactive barrier advertised: %s", recorder.Body.String())
	}
}
