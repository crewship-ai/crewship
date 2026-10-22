//go:build crewship_testbarrier && unix

package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/appengine"
)

func TestTaggedBarrierRefusesStageAndAdvertisesOnDev(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := t.TempDir()
	if err := os.WriteFile(filepath.Join(marker, "test-barrier-dev2.enabled"), []byte("slot=dev2 port=8082\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appengine.SetTestBarrierMarkerForTest(marker, os.Getuid(), func() int { return os.Getuid() + 1 }))
	t.Setenv(appengine.TestBarrierDirEnv, dir)
	t.Cleanup(func() {
		os.Unsetenv(appengine.TestBarrierDirEnv)
		_, _ = appengine.ConfigureTestBarrier(0)
	})
	for _, slot := range []string{"stage", "prod"} {
		t.Setenv(appengine.TestBarrierSlotEnv, slot)
		if err := (&Server{}).Start(context.Background()); err == nil || !strings.Contains(err.Error(), "test barrier") {
			t.Fatalf("%s start: %v", slot, err)
		}
	}
	t.Setenv(appengine.TestBarrierSlotEnv, "dev2")
	// The marker names port 8082: another instance on the same host (stage
	// shares /etc/crewship) must not activate it.
	if err := startGridCraftTestBarrier(nil, 8084); err == nil {
		t.Fatal("marker for :8082 activated an instance on :8084")
	}
	if err := startGridCraftTestBarrier(nil, 8082); err != nil {
		t.Fatal(err)
	}
	s := newTestServer()
	recorder := httptest.NewRecorder()
	s.mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/healthz", nil))
	if !strings.Contains(recorder.Body.String(), `"test_barrier":true`) {
		t.Fatalf("active barrier not advertised: %s", recorder.Body.String())
	}
}
