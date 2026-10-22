//go:build crewship_testbarrier && unix

package server

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func TestTaggedBarrierRefusesStageAndAdvertisesOnDev(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(appengine.TestBarrierDirEnv, dir)
	t.Cleanup(func() {
		os.Unsetenv(appengine.TestBarrierDirEnv)
		_, _ = appengine.ConfigureTestBarrier()
	})
	for _, slot := range []string{"stage", "prod"} {
		t.Setenv(appengine.TestBarrierSlotEnv, slot)
		if err := (&Server{}).Start(context.Background()); err == nil || !strings.Contains(err.Error(), "test barrier") {
			t.Fatalf("%s start: %v", slot, err)
		}
	}
	t.Setenv(appengine.TestBarrierSlotEnv, "dev2")
	if err := startGridCraftTestBarrier(nil); err != nil {
		t.Fatal(err)
	}
	s := newTestServer()
	recorder := httptest.NewRecorder()
	s.mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))
	if !strings.Contains(recorder.Body.String(), `"test_barrier":true`) {
		t.Fatalf("active barrier not advertised: %s", recorder.Body.String())
	}
}
