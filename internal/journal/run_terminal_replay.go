package journal

import (
	"context"
	"fmt"
	"time"
)

// ReplaySyncEmitter records a stable projection and a retention-independent
// receipt together. It is required before acknowledging protected output.
type ReplaySyncEmitter interface {
	EmitReplaySync(context.Context, Entry) (string, bool, error)
}

// RunTerminalReplay provides one immutable representation to both the live API
// and retained-output recovery. eventAt is the captured completion time, or
// the deterministic legacy fallback when historical capture has no such time.
func RunTerminalReplay(runID, workspaceID, agentID string, eventAt time.Time, kind EntryType, workOwned bool, exitCode *int, message *string, metadata map[string]any) Entry {
	id := "run-terminal:" + runID
	if workOwned {
		id = "work-terminal:" + runID
	}
	payload := map[string]any{}
	if exitCode != nil {
		payload["exit_code"] = *exitCode
	}
	if message != nil && *message != "" {
		payload["error_message"] = *message
	}
	if metadata != nil {
		payload["metadata"] = metadata
	}
	severity := SeverityInfo
	if kind == EntryRunFailed || kind == EntryRunTimeout {
		severity = SeverityError
	}
	short := runID
	if len(short) > 8 {
		short = short[:8]
	}
	return Entry{ID: id, WorkspaceID: workspaceID, AgentID: agentID, TS: eventAt, Type: kind,
		Severity: severity, ActorType: ActorSidecar, Summary: fmt.Sprintf("run %s %s", short, string(kind)[len("run."):]),
		Payload: payload, TraceID: runID, SpanID: id}
}

// RunTerminalEventTime selects durable captured time. Legacy absence retains
// the old deterministic timestamp; it is not proof of when execution finished.
func RunTerminalEventTime(startedAt time.Time, captured string) (time.Time, error) {
	if captured == "" {
		return startedAt, nil
	}
	completedAt, err := time.Parse(time.RFC3339Nano, captured)
	if err != nil || completedAt.IsZero() {
		return time.Time{}, fmt.Errorf("invalid captured run completion time")
	}
	return completedAt, nil
}
