package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// snapshotStubContainer is a minimal ContainerProvider whose Exec method
// returns canned output for the four probe scripts containerstate fires.
// Each probe is independent so a test can flip individual sources between
// runs to assert "hash changed → emit" / "hash same → skip".
type snapshotStubContainer struct {
	apt string
	pip string
	npm string
	os  string
}

func (s *snapshotStubContainer) reply(cfg provider.ExecConfig) string {
	if len(cfg.Cmd) < 3 || cfg.Cmd[0] != "sh" {
		return ""
	}
	script := cfg.Cmd[2]
	switch {
	case strings.Contains(script, "dpkg-query"):
		return s.apt
	case strings.Contains(script, "pip freeze") || strings.Contains(script, "pip3 freeze"):
		return s.pip
	case strings.Contains(script, "npm ls -g"):
		return s.npm
	case strings.Contains(script, "/etc/os-release"):
		return s.os
	default:
		return ""
	}
}

func (s *snapshotStubContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(s.reply(cfg)))}, nil
}

func (s *snapshotStubContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", nil
}
func (s *snapshotStubContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (s *snapshotStubContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (s *snapshotStubContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (s *snapshotStubContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (s *snapshotStubContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (s *snapshotStubContainer) CrewContainerName(slug string) string { return "test-" + slug }
func (s *snapshotStubContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	return nil
}

// TestRecordContainerSnapshot_EmitsAndDedups exercises the post-run
// container snapshot path: first call emits a container.snapshot entry;
// a second call with identical state must skip the emit (hash dedup);
// a third call after a state change must emit again. Without dedup, the
// journal would gain a heartbeat row per agent run and the "what
// actually changed?" signal drowns in noise.
func TestRecordContainerSnapshot_EmitsAndDedups(t *testing.T) {
	t.Parallel()
	stub := &snapshotStubContainer{
		apt: "git\t2.43.0-1\n",
		os:  "Ubuntu 24.04 LTS",
	}
	o := New(stub, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	req := AgentRunRequest{
		AgentID: "a1", AgentSlug: "alice",
		WorkspaceID: "ws", CrewID: "c1", CrewSlug: "team",
		ChatID: "chat", ContainerID: "ctr-1",
	}

	// First snapshot — must emit.
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := countSnapshots(rec); got != 1 {
		t.Fatalf("first run: want 1 container.snapshot entry, got %d", got)
	}

	// Identical state on the second run — hash matches the cached one,
	// so the orchestrator must skip the emit.
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := countSnapshots(rec); got != 1 {
		t.Fatalf("second run with identical state: want 1 (no new emit), got %d", got)
	}

	// State changed (an agent installed php) — hash differs, must emit.
	stub.apt = "git\t2.43.0-1\nphp\t8.3.1\n"
	o.recordContainerSnapshot(context.Background(), req, "ctr-1")
	if got := countSnapshots(rec); got != 2 {
		t.Fatalf("after install: want 2 entries, got %d", got)
	}

	// And payload of the latest must include both packages. The Emitter
	// stores the typed []containerstate.Package directly, so the assert
	// uses reflect-friendly len() rather than re-marshalling JSON.
	last := lastSnapshot(rec)
	if last == nil {
		t.Fatal("expected a snapshot entry")
	}
	type aptCounter interface{ Len() int }
	got := -1
	if pkgs, ok := last.Payload["apt"].([]any); ok {
		got = len(pkgs)
	} else {
		// Typed-slice path: probe via the counts payload field which
		// recordContainerSnapshot always populates.
		if counts, ok := last.Payload["counts"].(map[string]int); ok {
			got = counts["apt"]
		}
	}
	if got != 2 {
		t.Errorf("apt count after install: want 2, got %d (payload=%+v)", got, last.Payload)
	}
}

// TestRecordContainerSnapshot_NoContainer is a no-op when containerID is
// empty (e.g. coordinator agents that don't hold a container handle).
func TestRecordContainerSnapshot_NoContainer(t *testing.T) {
	t.Parallel()
	o := New(&snapshotStubContainer{}, newMemState(), slog.Default())
	rec := &chunkRecorder{}
	o.SetJournal(rec)

	o.recordContainerSnapshot(context.Background(), AgentRunRequest{}, "")
	if got := countSnapshots(rec); got != 0 {
		t.Errorf("empty containerID must skip emit, got %d entries", got)
	}
}

func countSnapshots(rec *chunkRecorder) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, e := range rec.entries {
		if e.Type == "container.snapshot" {
			n++
		}
	}
	return n
}

func lastSnapshot(rec *chunkRecorder) *JournalEntry {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i := len(rec.entries) - 1; i >= 0; i-- {
		if rec.entries[i].Type == "container.snapshot" {
			return &rec.entries[i]
		}
	}
	return nil
}
