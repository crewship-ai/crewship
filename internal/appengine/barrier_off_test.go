//go:build !crewship_testbarrier || !unix

package appengine

import (
	"context"
	"os"
	"testing"
	"time"
)

// Normal builds carry no barrier: even a fully configured environment neither
// activates it nor delays or records a barrier- operation.
func TestTestBarrierIsNoOpWithoutBuildTag(t *testing.T) {
	if barrierCompiledIn {
		t.Fatal("barrier compiled into a normal build")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TestBarrierDirEnv, dir)
	t.Setenv(TestBarrierSlotEnv, "dev2")
	if active, err := ConfigureTestBarrier(8082); active || err != nil || TestBarrierActive() {
		t.Fatalf("active=%v err=%v", active, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	for _, point := range []barrierPoint{barrierPreCommit, barrierPostCommit} {
		if err := testBarrier(ctx, point, Key{"ws", "page"}, "user:owner", TestBarrierMutationPrefix+"1"); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("untagged barrier waited")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("untagged barrier wrote files: %v %v", entries, err)
	}
}
