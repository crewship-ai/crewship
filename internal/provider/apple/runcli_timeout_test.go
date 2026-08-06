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

// The generic 5-minute bound was measured against the wrong thing. A
// `container create` from a provisioned devcontainer image took 454s on an
// M-series Mac — unpacking the image's root filesystem into a fresh VM disk is
// real work, not a wedge — and the bound killed it, failing a crew whose
// container was being built correctly.
//
// This is the third bound on this branch set without measuring the legitimate
// case first, so it is pinned here rather than left to a comment (#1779).
func TestRunCLI_HeavyOperationsGetALongerBound(t *testing.T) {
	for _, op := range []string{"create", "run", "start", "pull", "build", "cp"} {
		if !heavyCLIOps[op] {
			t.Errorf("%q moves real data and must not share the short bound", op)
		}
	}
	if heavyCLITimeout <= defaultCLITimeout {
		t.Errorf("heavy bound %s must exceed the generic %s", heavyCLITimeout, defaultCLITimeout)
	}
	// 454s measured; the bound has to clear it with room for a slower host.
	if heavyCLITimeout < 10*time.Minute {
		t.Errorf("heavy bound %s is under the measured 454s create with no headroom", heavyCLITimeout)
	}
}

// Queries stay short: a wedged `image list` must not hold a crew start for
// twenty minutes.
func TestRunCLI_QueriesKeepTheShortBound(t *testing.T) {
	for _, op := range []string{"list", "inspect", "status", "delete"} {
		if heavyCLIOps[op] {
			t.Errorf("%q is a query and must keep the short bound", op)
		}
	}
}
