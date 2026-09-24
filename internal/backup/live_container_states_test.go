//go:build livedocker

// Live proof for #2612: a workspace export must work for a crew
// container in any prior state and must leave that state alone.
//
// The unit suite covers the branch logic with fakes; what only a real
// daemon can prove is the two facts the fix turns on:
//
//   - the archive API (GET /containers/{id}/archive) reads a STOPPED
//     container's filesystem fine, so a stopped crew can be backed up
//     without being started — the pause that used to gate collection is
//     not what makes the copy possible, only what makes it consistent;
//   - an already-paused container stays paused after collection — the
//     backup must not resume a pause it does not own (#2612: the
//     pre-fix code unpaused unconditionally).
//
//	go test -tags livedocker -run TestLive_CollectCrew_ContainerStates -v ./internal/backup/
package backup

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// liveCollect runs CollectCrew into a throwaway buffer and returns the
// capture. Deliberately not collectToPayload (the round-trip helper):
// this test never restores, so extracting the payload to disk would be
// setup for an assertion that never comes.
func liveCollect(ctx context.Context, t *testing.T, ops DockerOps, crew CrewTarget, level ScopeLevel) CrewCapture {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewTarZstWriter(&buf)
	if err != nil {
		t.Fatalf("tar writer: %v", err)
	}
	capture, err := CollectCrew(ctx, ops, w, crew, level)
	if cerr := w.Close(); err == nil && cerr != nil {
		t.Fatalf("close payload writer: %v", cerr)
	}
	if err != nil {
		t.Fatalf("CollectCrew: %v", err)
	}
	return capture
}

// liveState reports (running, paused) as the daemon sees the container.
func liveState(ctx context.Context, t *testing.T, lc *liveCrew) (running, paused bool) {
	t.Helper()
	insp, err := lc.cli.ContainerInspect(ctx, lc.id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", lc.id, err)
	}
	if insp.Container.State == nil {
		t.Fatalf("inspect %s: no state returned", lc.id)
	}
	return insp.Container.State.Running, insp.Container.State.Paused
}

func TestLive_CollectCrew_ContainerStates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cli := newLiveClient(t)
	lc := startLiveCrew(ctx, t, cli, "states2612", true)
	ops := &MobyDockerOps{Client: cli}
	crew := CrewTarget{ID: "live-crew", Slug: "engineering", ContainerID: lc.id}

	// Seed one file per core section while the container is up, so every
	// collection below has something to find and the file counts are
	// evidence of a real copy rather than a vacuous success.
	lc.mustSh(ctx, t, "1001:1001",
		`echo workspace-probe > /workspace/probe.txt && mkdir -p /crew/shared/.memory && echo memory-probe > /crew/shared/.memory/note.md && echo output-probe > /output/probe.txt`)

	// --- 1. Running: collect, and the container is running again after.
	capture := liveCollect(ctx, t, ops, crew, ScopeLevelQuick)
	if capture.WorkspaceFiles == 0 || capture.CrewMemoryFiles == 0 || capture.OutputFiles == 0 {
		t.Fatalf("running: capture looks empty: %+v", capture)
	}
	if running, paused := liveState(ctx, t, lc); !running || paused {
		t.Errorf("running case: container must be running and not paused after collection, got running=%v paused=%v", running, paused)
	}

	// --- 2. Already paused by someone else: collect, and the pause
	// survives. The pre-fix code unpaused here — resuming a pause the
	// backup never made.
	if _, err := cli.ContainerPause(ctx, lc.id, client.ContainerPauseOptions{}); err != nil {
		t.Fatalf("external pause: %v", err)
	}
	capture = liveCollect(ctx, t, ops, crew, ScopeLevelQuick)
	if capture.WorkspaceFiles == 0 || capture.CrewMemoryFiles == 0 {
		t.Fatalf("already-paused: capture looks empty: %+v", capture)
	}
	if running, paused := liveState(ctx, t, lc); paused != true || running != true {
		t.Errorf("already-paused case: container must still be paused-and-running after collection, got running=%v paused=%v — the backup resumed a pause it did not own", running, paused)
	}
	// Cleanup unpause: tolerate "is not paused" — on pre-fix code the
	// backup itself already resumed the container, which is exactly the
	// defect this case documents.
	if _, err := cli.ContainerUnpause(ctx, lc.id, client.ContainerUnpauseOptions{}); err != nil && !strings.Contains(err.Error(), "not paused") {
		t.Fatalf("external unpause (cleanup): %v", err)
	}

	// --- 3. Stopped: collect without starting the container, capture
	// real files, and leave it stopped. The pre-fix code failed the
	// whole export here ("container ... is not running").
	stopCtx, stopCancel := context.WithTimeout(ctx, 60*time.Second)
	_, stopErr := cli.ContainerStop(stopCtx, lc.id, client.ContainerStopOptions{})
	stopCancel()
	if stopErr != nil {
		t.Fatalf("stop container: %v", stopErr)
	}
	capture = liveCollect(ctx, t, ops, crew, ScopeLevelQuick)
	if capture.WorkspaceFiles == 0 || capture.CrewMemoryFiles == 0 || capture.OutputFiles == 0 {
		t.Fatalf("stopped: capture looks empty — stopped crew lost its files: %+v", capture)
	}
	if running, paused := liveState(ctx, t, lc); running || paused {
		t.Errorf("stopped case: container must stay stopped after collection, got running=%v paused=%v — the backup changed the container's state", running, paused)
	}
}
