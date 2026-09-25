package backup

// #2612 regression tests: CollectCrew must work for a crew container in
// any prior state — running, already paused, stopped — must leave that
// state as it found it, and a collection error must neither produce a
// capture that looks complete nor leave a running container paused.
//
// The live proof against a real daemon lives in
// live_container_states_test.go (build tag livedocker); these unit
// cases pin the collector wiring down where they run on every CI pass.

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestCollectCrew_ContainerStates(t *testing.T) {
	ctx := context.Background()

	newCrew := func() CrewTarget {
		return CrewTarget{ID: "crew1", Slug: "engineering", ContainerID: "crewship-team-engineering"}
	}
	seed := func() *fakeDockerOps {
		f := newFakeDockerOps()
		f.workspace["probe.txt"] = []byte("workspace-probe")
		return f
	}
	collect := func(f *fakeDockerOps) (CrewCapture, error) {
		var buf bytes.Buffer
		w, err := NewTarZstWriter(&buf)
		if err != nil {
			t.Fatalf("tar writer: %v", err)
		}
		cap, cerr := CollectCrew(ctx, f, w, newCrew(), ScopeLevelQuick)
		if werr := w.Close(); cerr == nil && werr != nil {
			t.Fatalf("close writer: %v", werr)
		}
		return cap, cerr
	}

	t.Run("running: collected, then resumed", func(t *testing.T) {
		f := seed()
		cap, err := collect(f)
		if err != nil {
			t.Fatalf("CollectCrew: %v", err)
		}
		if cap.WorkspaceFiles != 1 {
			t.Fatalf("workspace files = %d, want 1", cap.WorkspaceFiles)
		}
		if f.paused {
			t.Error("container left paused after collection of a running crew")
		}
		if f.unpauseCalls != 1 {
			t.Errorf("unpause calls = %d, want 1", f.unpauseCalls)
		}
	})

	t.Run("already paused: collected, pause survives", func(t *testing.T) {
		f := seed()
		f.pauseErr = ErrAlreadyPaused
		cap, err := collect(f)
		if err != nil {
			t.Fatalf("CollectCrew: %v", err)
		}
		if cap.WorkspaceFiles != 1 {
			t.Fatalf("workspace files = %d, want 1 — an externally paused crew must still back up", cap.WorkspaceFiles)
		}
		if !f.paused {
			t.Error("backup resumed a pause it did not own")
		}
		if f.unpauseCalls != 0 {
			t.Errorf("unpause calls = %d, want 0 — a pre-existing pause must be left alone", f.unpauseCalls)
		}
	})

	t.Run("stopped: collected without pause, nothing resumed", func(t *testing.T) {
		f := seed()
		f.pauseErr = ErrNotRunning
		cap, err := collect(f)
		if err != nil {
			t.Fatalf("CollectCrew: %v — a stopped crew container must back up without being started", err)
		}
		if cap.WorkspaceFiles != 1 {
			t.Fatalf("workspace files = %d, want 1", cap.WorkspaceFiles)
		}
		if f.unpauseCalls != 0 {
			t.Errorf("unpause calls = %d, want 0 — nothing was paused", f.unpauseCalls)
		}
	})

	t.Run("collection error: running crew is resumed, error surfaces", func(t *testing.T) {
		f := seed()
		f.copyFromErr = errors.New("daemon unreachable") // hard error, not a skippable missing path
		_, err := collect(f)
		if err == nil {
			t.Fatal("expected collection error to fail the backup — an incomplete export must not look successful")
		}
		if f.paused {
			t.Error("container left paused after a failed collection")
		}
		if f.unpauseCalls != 1 {
			t.Errorf("unpause calls = %d, want 1 — cleanup must run on collection errors", f.unpauseCalls)
		}
	})

	t.Run("collection error: stopped crew propagates error, no unpause", func(t *testing.T) {
		f := seed()
		f.pauseErr = ErrNotRunning
		f.copyFromErr = errors.New("daemon unreachable")
		_, err := collect(f)
		if err == nil {
			t.Fatal("expected collection error to fail the backup")
		}
		if f.unpauseCalls != 0 {
			t.Errorf("unpause calls = %d, want 0 — nothing was paused", f.unpauseCalls)
		}
	})

	t.Run("missing container: explicit failure before collection", func(t *testing.T) {
		f := seed()
		f.pauseErr = errors.New("backup: docker pause c1: No such container")
		f.copyFromCalls = 0
		_, err := collect(f)
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("No such container")) {
			t.Fatalf("expected missing-container error, got %v", err)
		}
		if f.copyFromCalls != 0 {
			t.Errorf("copy calls = %d, want 0 — nothing may be collected from a vanished container", f.copyFromCalls)
		}
	})
}
