package apple

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Apple's CLI can wedge: a `container create` sat at 6+ minutes with the same
// argument vector that completes in seconds when run by hand, and the build
// side of this provider showed the same shape. runCLI passed the caller's
// context straight to exec.CommandContext, so a caller without a deadline —
// which is most of them — waited forever and took the crew start with it.
//
// A bound turns "hangs forever" into "fails and says why", which is the only
// version of this a user can act on (#1779).
func TestRunCLIWithin_BoundsAWedgedCommand(t *testing.T) {
	installFakeContainer(t, "sleep 30")

	start := time.Now()
	_, err := runCLIWithin(context.Background(), 300*time.Millisecond, "create", "x")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a wedged CLI call must fail, not hang")
	}
	if elapsed > 10*time.Second {
		t.Errorf("returned after %s — the bound did not apply", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error should say it timed out, got %q", err)
	}
}

// Killing only the CLI leaves its children holding the pipes, which is how the
// build side stayed stuck after its watchdog fired.
func TestRunCLIWithin_KillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "child-alive")
	// The child outlives its parent unless the whole group is signalled.
	installFakeContainer(t, "(sleep 5; touch "+marker+") & sleep 30")

	_, err := runCLIWithin(context.Background(), 200*time.Millisecond, "create", "x")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	time.Sleep(2 * time.Second)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the child survived the kill — the process group was not signalled")
	}
}

func TestRunCLIWithin_PassesThroughOnSuccess(t *testing.T) {
	installFakeContainer(t, "echo ok")

	out, err := runCLIWithin(context.Background(), 5*time.Second, "image", "list")
	if err != nil {
		t.Fatalf("runCLIWithin: %v", err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("output = %q, want ok", out)
	}
}
