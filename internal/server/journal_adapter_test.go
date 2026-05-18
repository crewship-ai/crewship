package server

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ---------------------------------------------------------------------------
// journal_adapter.go — orchestratorJournalAdapter.Emit + severityOrDefault
// + actorOrDefault.
//
// The orchestrator owns a narrow JournalEmitter interface to avoid a
// cycle with internal/journal. This adapter lives in the server package
// (the sole place that knows both worlds) and translates between the
// two at every emit. The defaults filling in empty Severity/ActorType
// let orchestrator callers stay terse for routine events without
// repeating string literals at every call site.
// ---------------------------------------------------------------------------

// recordingJournalEmitter is a tiny journal.Emitter that captures the
// last Emit call so tests can assert which fields were translated.
type recordingJournalEmitter struct {
	calls     int
	lastEntry journal.Entry
	returnID  string
	returnErr error
}

func (r *recordingJournalEmitter) Emit(_ context.Context, e journal.Entry) (string, error) {
	r.calls++
	r.lastEntry = e
	return r.returnID, r.returnErr
}
func (r *recordingJournalEmitter) Flush(_ context.Context) error { return nil }

// ---- Emit ----

func TestOrchestratorJournalAdapter_Emit_NilEmitter_NoOp(t *testing.T) {
	// Source: nil emitter is a documented no-op — handlers in the
	// orchestrator can emit unconditionally without nil-checking the
	// adapter. Pin that ("", nil) comes back AND no panic fires.
	a := newOrchestratorJournalAdapter(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil emitter Emit panicked: %v", r)
		}
	}()
	id, err := a.Emit(context.Background(), orchestrator.JournalEntry{Type: "test.event"})
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty", id)
	}
}

func TestOrchestratorJournalAdapter_Emit_TranslatesAllFields(t *testing.T) {
	// Field-by-field round-trip pin — a regression that dropped a field
	// (e.g. ActorID, MissionID, Refs) would silently lose audit context
	// for every orchestrator-emitted entry. Easier to catch here than
	// in a downstream "why is MissionID missing on Crow's Nest" bug.
	rec := &recordingJournalEmitter{returnID: "emitted-id"}
	a := newOrchestratorJournalAdapter(rec)
	in := orchestrator.JournalEntry{
		WorkspaceID: "ws-1",
		CrewID:      "crew-1",
		AgentID:     "agent-1",
		MissionID:   "mission-1",
		Type:        "tool.call",
		Severity:    "warn",
		ActorType:   "sidecar",
		ActorID:     "actor-1",
		Summary:     "shell exec",
		Payload:     map[string]any{"cmd": "ls"},
		Refs:        map[string]any{"trace_id": "trace-x"},
	}
	id, err := a.Emit(context.Background(), in)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if id != "emitted-id" {
		t.Errorf("id = %q, want emitted-id (must return whatever the wrapped emitter returns)", id)
	}
	if rec.calls != 1 {
		t.Fatalf("emitter called %d times, want 1", rec.calls)
	}
	e := rec.lastEntry
	if e.WorkspaceID != "ws-1" {
		t.Errorf("WorkspaceID = %q", e.WorkspaceID)
	}
	if e.CrewID != "crew-1" {
		t.Errorf("CrewID = %q", e.CrewID)
	}
	if e.AgentID != "agent-1" {
		t.Errorf("AgentID = %q", e.AgentID)
	}
	if e.MissionID != "mission-1" {
		t.Errorf("MissionID = %q", e.MissionID)
	}
	if e.Type != journal.EntryType("tool.call") {
		t.Errorf("Type = %q, want tool.call", e.Type)
	}
	if e.Severity != journal.SeverityWarn {
		t.Errorf("Severity = %q, want warn (translated from string)", e.Severity)
	}
	if e.ActorType != journal.ActorSidecar {
		t.Errorf("ActorType = %q, want sidecar (translated from string)", e.ActorType)
	}
	if e.ActorID != "actor-1" {
		t.Errorf("ActorID = %q", e.ActorID)
	}
	if e.Summary != "shell exec" {
		t.Errorf("Summary = %q", e.Summary)
	}
	if e.Payload["cmd"] != "ls" {
		t.Errorf("Payload not forwarded: %+v", e.Payload)
	}
	if e.Refs["trace_id"] != "trace-x" {
		t.Errorf("Refs not forwarded: %+v", e.Refs)
	}
}

func TestOrchestratorJournalAdapter_Emit_AppliesDefaults_EmptySeverityActorType(t *testing.T) {
	// Source comment: defaults fill in "info" + "orchestrator" so
	// orchestrator callers can stay terse for routine events. Pin both
	// defaults fire when the corresponding string is empty.
	rec := &recordingJournalEmitter{}
	a := newOrchestratorJournalAdapter(rec)
	_, _ = a.Emit(context.Background(), orchestrator.JournalEntry{
		WorkspaceID: "ws-1",
		Type:        "exec.command",
		// Severity + ActorType deliberately empty
	})
	if rec.lastEntry.Severity != journal.SeverityInfo {
		t.Errorf("Severity = %q, want %q (empty → info default)", rec.lastEntry.Severity, journal.SeverityInfo)
	}
	if rec.lastEntry.ActorType != journal.ActorOrchestrator {
		t.Errorf("ActorType = %q, want %q (empty → orchestrator default)", rec.lastEntry.ActorType, journal.ActorOrchestrator)
	}
}

func TestOrchestratorJournalAdapter_Emit_PropagatesEmitterError(t *testing.T) {
	// A real journal.Emitter can return errors (run.* unwired emitter,
	// DB write failure). The adapter must propagate the error unchanged
	// so the orchestrator caller can decide whether to 500 or retry.
	want := errors.New("downstream emit failed")
	rec := &recordingJournalEmitter{returnErr: want}
	a := newOrchestratorJournalAdapter(rec)
	_, err := a.Emit(context.Background(), orchestrator.JournalEntry{
		WorkspaceID: "ws-1",
		Type:        "run.started",
	})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want errors.Is(err, %v)", err, want)
	}
}

// ---- severityOrDefault ----

func TestSeverityOrDefault(t *testing.T) {
	cases := []struct {
		in   string
		want journal.Severity
	}{
		{"", journal.SeverityInfo},
		{"info", journal.SeverityInfo},
		{"warn", journal.SeverityWarn},
		{"error", journal.SeverityError},
		{"notice", journal.SeverityNotice},
		// Source casts the string verbatim — an unknown value passes
		// through. Pin this so a refactor that introduces a "validate
		// or default" guard surfaces as an explicit behavior change.
		{"unrecognized-future-value", journal.Severity("unrecognized-future-value")},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := severityOrDefault(tc.in); got != tc.want {
				t.Errorf("severityOrDefault(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---- actorOrDefault ----

func TestActorOrDefault(t *testing.T) {
	cases := []struct {
		in   string
		want journal.ActorType
	}{
		{"", journal.ActorOrchestrator},
		{"orchestrator", journal.ActorOrchestrator},
		{"agent", journal.ActorAgent},
		{"sidecar", journal.ActorSidecar},
		{"system", journal.ActorSystem},
		{"keeper", journal.ActorKeeper},
		{"user", journal.ActorUser},
		// Same pass-through contract as severityOrDefault.
		{"future-actor", journal.ActorType("future-actor")},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := actorOrDefault(tc.in); got != tc.want {
				t.Errorf("actorOrDefault(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
