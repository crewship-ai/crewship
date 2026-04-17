package quartermaster

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Extract pulls every journal entry for a mission, filters it down to the
// entry types that carry trajectory signal, and projects each into a
// typed TrajectoryStep. Entries are returned oldest-first (time order) so
// downstream metric code can walk them as a timeline.
//
// Skipped types (too noisy or not a step):
//   - exec.output_chunk (raw stdout/stderr — covered by exec.command)
//   - container.metrics (periodic samples)
//   - network.port_opened/port_closed (infrastructure noise)
//
// Kept types:
//   - assignment.{created,running,completed,failed}
//   - exec.command (with exit_code / success projection)
//   - llm.call (with cost + tokens)
//   - mission.status_change
//   - keeper.decision
//   - guardrail.input_blocked / guardrail.output_blocked
func Extract(ctx context.Context, db *sql.DB, workspaceID, missionID string) ([]TrajectoryStep, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("quartermaster: workspace_id required")
	}
	if missionID == "" {
		return nil, fmt.Errorf("quartermaster: mission_id required")
	}

	// Page through journal.List until we have every entry for this mission.
	var entries []journal.Entry
	cursor := ""
	for {
		page, next, err := journal.List(ctx, db, journal.Query{
			WorkspaceID: workspaceID,
			MissionID:   missionID,
			Limit:       500,
			Cursor:      cursor,
		})
		if err != nil {
			return nil, fmt.Errorf("quartermaster: list journal: %w", err)
		}
		entries = append(entries, page...)
		if next == "" || len(page) == 0 {
			break
		}
		cursor = next
	}

	// List returns newest-first; re-sort oldest-first for a natural timeline.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].TS.Equal(entries[j].TS) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].TS.Before(entries[j].TS)
	})

	steps := make([]TrajectoryStep, 0, len(entries))
	for _, e := range entries {
		step, ok := projectEntry(e)
		if !ok {
			continue
		}
		step.Index = len(steps)
		steps = append(steps, step)
	}
	return steps, nil
}

// projectEntry maps a journal entry to a TrajectoryStep, returning
// ok=false for entry types we don't treat as trajectory steps.
func projectEntry(e journal.Entry) (TrajectoryStep, bool) {
	step := TrajectoryStep{
		EntryID:   e.ID,
		EntryType: string(e.Type),
		Summary:   e.Summary,
	}

	switch e.Type {
	case journal.EntryAssignmentCreate,
		journal.EntryAssignmentRun:
		// Pending/in-flight assignments count as steps but have no
		// success signal yet.
		step.Success = true
		return step, true

	case journal.EntryAssignmentDone:
		step.Success = true
		return step, true

	case journal.EntryAssignmentFail:
		step.Success = false
		return step, true

	case journal.EntryExecCommand:
		// exit_code == 0 → success. Missing exit_code defaults to success
		// so we don't penalize old rows that pre-date the payload field.
		step.ToolName = payloadString(e.Payload, "command", "tool", "name")
		step.Success = payloadExitOK(e.Payload)
		step.ElapsedMs = payloadInt(e.Payload, "elapsed_ms", "duration_ms")
		return step, true

	case journal.EntryLLMCall:
		step.ToolName = payloadString(e.Payload, "model", "provider")
		step.Success = true // model returned a response; the call itself succeeded
		step.TokenCost = payloadInt(e.Payload, "total_tokens", "tokens")
		step.ElapsedMs = payloadInt(e.Payload, "elapsed_ms", "duration_ms")
		return step, true

	case journal.EntryMissionStatus:
		step.Success = true
		step.ToolName = payloadString(e.Payload, "to_status", "status")
		return step, true

	case journal.EntryKeeperDecision:
		step.ToolName = payloadString(e.Payload, "credential_id", "decision")
		step.Success = payloadKeeperApproved(e.Payload)
		return step, true

	case journal.EntryGuardrailInput,
		journal.EntryGuardrailOutput:
		// Guardrail blocks count as steps (something happened) but are
		// failures by construction.
		step.Success = false
		return step, true

	case journal.EntryPeerEscalation:
		// Kept because escalation loops are a known MAST failure mode.
		step.Success = true
		return step, true

	case journal.EntryBudgetExceed,
		journal.EntryBudgetWarning:
		step.Success = false
		return step, true

	default:
		return TrajectoryStep{}, false
	}
}

// payloadString returns the first non-empty string value under the given
// keys in the payload map, or "" if none are present.
func payloadString(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := p[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// payloadInt returns the first integer-like value under the given keys.
// JSON numbers unmarshal as float64, so we accept both.
func payloadInt(p map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := p[k]; ok {
			switch n := v.(type) {
			case float64:
				return int(n)
			case int:
				return n
			case int64:
				return int(n)
			}
		}
	}
	return 0
}

// payloadExitOK returns true when the payload's exit_code is absent or zero.
func payloadExitOK(p map[string]any) bool {
	if p == nil {
		return true
	}
	v, ok := p["exit_code"]
	if !ok {
		return true
	}
	switch n := v.(type) {
	case float64:
		return n == 0
	case int:
		return n == 0
	case int64:
		return n == 0
	case bool:
		return n // permissive
	}
	return true
}

// payloadKeeperApproved treats the keeper decision payload as "success"
// when decision == "allow" (case-insensitive). Anything else (deny,
// escalate, error) is treated as a failed tool call.
func payloadKeeperApproved(p map[string]any) bool {
	if p == nil {
		return false
	}
	if v, ok := p["decision"].(string); ok {
		switch v {
		case "allow", "ALLOW", "Allow", "approved", "approve":
			return true
		}
	}
	if v, ok := p["approved"].(bool); ok {
		return v
	}
	return false
}
