package orchestrator

// #1160 part 1: a detected stale sidecar must land on a durable,
// operator-watchable channel — a severity:error journal entry — not
// just a single logger.Error to stdout (the channel class nobody
// watched when #1008 happened).

import (
	"context"
	"log/slog"
	"testing"
)

func TestEmitStaleSidecarSignal_EmitsErrorJournalEntry(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)

	req := AgentRunRequest{
		WorkspaceID: "ws1",
		CrewID:      "crew1",
		AgentID:     "a1",
		AgentSlug:   "researcher",
		ContainerID: "abcdef0123456789",
	}
	o.emitStaleSidecarSignal(context.Background(), req, "oldhash12345")

	if len(cj.entries) != 1 {
		t.Fatalf("expected exactly one journal entry, got %d", len(cj.entries))
	}
	e := cj.entries[0]
	if e.Type != "sidecar.stale" {
		t.Errorf("Type = %q, want sidecar.stale", e.Type)
	}
	if e.Severity != "error" {
		t.Errorf("Severity = %q, want error", e.Severity)
	}
	if e.WorkspaceID != "ws1" || e.CrewID != "crew1" || e.AgentID != "a1" {
		t.Errorf("scope not propagated onto entry: %+v", e)
	}
	if got, _ := e.Payload["running_sidecar_hash"].(string); got != "oldhash12345" {
		t.Errorf("running_sidecar_hash payload = %q, want oldhash12345", got)
	}
	// Container id is truncated to a 12-char prefix for the payload, matching
	// the log-line convention (full ids are noise and leak nothing useful).
	if got, _ := e.Payload["container_id"].(string); got != "abcdef012345" {
		t.Errorf("container_id payload = %q, want 12-char prefix", got)
	}
	// The remediation must be discoverable from the entry alone so an
	// operator triaging the activity feed knows the fix without grepping code.
	if got, _ := e.Payload["remediation"].(string); got == "" {
		t.Errorf("expected a remediation hint in payload, got none: %+v", e.Payload)
	}
}

// A short container id must not slice-panic when truncating to 12 chars.
func TestEmitStaleSidecarSignal_ShortContainerID_NoPanic(t *testing.T) {
	o := New(nil, nil, slog.Default())
	cj := &captureJournal{}
	o.SetJournal(cj)
	req := AgentRunRequest{WorkspaceID: "ws1", AgentID: "a1", ContainerID: "abc"}
	o.emitStaleSidecarSignal(context.Background(), req, "h")
	if len(cj.entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(cj.entries))
	}
	if got, _ := cj.entries[0].Payload["container_id"].(string); got != "abc" {
		t.Errorf("short container_id payload = %q, want abc", got)
	}
}
