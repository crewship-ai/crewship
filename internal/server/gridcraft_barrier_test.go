//go:build !crewship_testbarrier || !unix

package server

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

// Normal builds never advertise a test barrier, even with its environment set.
// Tagged builds are covered by gridcraft_barrier_tagged_test.go.
func TestHealthzOmitsTestBarrierWhenInactive(t *testing.T) {
	t.Setenv(appengine.TestBarrierDirEnv, t.TempDir())
	t.Setenv(appengine.TestBarrierSlotEnv, "dev2")
	// Whatever this build does with the environment, leave no global barrier
	// state behind for later tests (cleanups run before t.Setenv restores).
	t.Cleanup(func() {
		os.Unsetenv(appengine.TestBarrierDirEnv)
		_, _ = appengine.ConfigureTestBarrier()
	})
	s := newTestServer()
	if err := startGridCraftTestBarrier(nil); err != nil {
		t.Fatal(err)
	}
	if appengine.TestBarrierActive() {
		t.Fatal("barrier active in an untagged build")
	}
	recorder := httptest.NewRecorder()
	s.mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))
	if strings.Contains(recorder.Body.String(), "test_barrier") {
		t.Fatalf("inactive barrier advertised: %s", recorder.Body.String())
	}
}
