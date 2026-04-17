package server

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// captureEmitter records every Emit call so tests can assert on the
// sequence of journal entries produced by the scanner. It deliberately
// copies the entry on write so later in-place mutation by the caller
// can't corrupt the recorded history.
type captureEmitter struct {
	entries []journal.Entry
}

func (c *captureEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	c.entries = append(c.entries, e)
	return "ok", nil
}

func (c *captureEmitter) Flush(_ context.Context) error { return nil }

func TestDiffAndEmit_OpensCloses(t *testing.T) {
	t.Parallel()

	prev := map[string]exposureSnapshot{
		"e1": {WorkspaceID: "ws", CrewID: "c1", AgentID: "a1", ContainerID: "cid1", ContainerPort: 8080},
	}
	current := map[string]exposureSnapshot{
		"e1": {WorkspaceID: "ws", CrewID: "c1", AgentID: "a1", ContainerID: "cid1", ContainerPort: 8080},
		"e2": {WorkspaceID: "ws", CrewID: "c2", AgentID: "a2", ContainerID: "cid2", ContainerPort: 3000},
	}

	cap := &captureEmitter{}
	diffAndEmit(context.Background(), cap, prev, current)

	if len(cap.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(cap.entries))
	}
	if cap.entries[0].Type != journal.EntryNetworkPortOpen {
		t.Errorf("expected port_opened, got %s", cap.entries[0].Type)
	}
	if cap.entries[0].CrewID != "c2" {
		t.Errorf("expected crew c2, got %s", cap.entries[0].CrewID)
	}
}

func TestDiffAndEmit_CloseOnDisappearance(t *testing.T) {
	t.Parallel()

	prev := map[string]exposureSnapshot{
		"e1": {WorkspaceID: "ws", CrewID: "c1", AgentID: "a1", ContainerID: "cid1", ContainerPort: 8080},
	}
	current := map[string]exposureSnapshot{}

	cap := &captureEmitter{}
	diffAndEmit(context.Background(), cap, prev, current)

	if len(cap.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(cap.entries))
	}
	if cap.entries[0].Type != journal.EntryNetworkPortClose {
		t.Errorf("expected port_closed, got %s", cap.entries[0].Type)
	}
}

func TestDiffAndEmit_NoOpOnIdenticalMaps(t *testing.T) {
	t.Parallel()

	snap := exposureSnapshot{WorkspaceID: "ws", CrewID: "c1", AgentID: "a1", ContainerID: "cid1", ContainerPort: 22}
	prev := map[string]exposureSnapshot{"e1": snap}
	current := map[string]exposureSnapshot{"e1": snap}

	cap := &captureEmitter{}
	diffAndEmit(context.Background(), cap, prev, current)

	if len(cap.entries) != 0 {
		t.Errorf("expected no entries for identical snapshots, got %d", len(cap.entries))
	}
}

func TestDiffAndEmit_NilEmitter(t *testing.T) {
	t.Parallel()
	// Must not panic when journal is nil — the scanner is wired
	// optimistically and callers shouldn't need to gate.
	diffAndEmit(context.Background(), nil,
		map[string]exposureSnapshot{},
		map[string]exposureSnapshot{"e1": {WorkspaceID: "ws", ContainerPort: 80}})
}

func TestFormatPort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want string
	}{
		{80, "80"},
		{8080, "8080"},
		{65535, "65535"},
		{1, "1"},
		{0, "?"},
		{-1, "?"},
	}
	for _, c := range cases {
		if got := formatPort(c.in); got != c.want {
			t.Errorf("formatPort(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortContainerID(t *testing.T) {
	t.Parallel()
	if got := shortContainerID("abcdefghijkl1234"); got != "abcdefghijkl" {
		t.Errorf("expected truncation, got %q", got)
	}
	if got := shortContainerID("abc"); got != "abc" {
		t.Errorf("expected pass-through, got %q", got)
	}
	if got := shortContainerID(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
