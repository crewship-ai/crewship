package quartermaster

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestExtract_RequiresIDs pins the input validation: both workspace and
// mission IDs are mandatory — a missing scope must never silently widen
// the journal query.
func TestExtract_RequiresIDs(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	if _, err := Extract(context.Background(), db, "", "m1"); err == nil {
		t.Error("empty workspace_id accepted")
	}
	if _, err := Extract(context.Background(), db, "ws_test", ""); err == nil {
		t.Error("empty mission_id accepted")
	}
}

// TestExtract_PaginatesBeyondOnePage seeds more entries than the 500-row
// page Extract requests from journal.List, proving the cursor loop pages
// through the full mission history and that Index stays a dense 0..n-1
// timeline ordered oldest-first.
func TestExtract_PaginatesBeyondOnePage(t *testing.T) {
	db := openDB(t)
	defer db.Close()

	const total = 502
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < total; i++ {
		seed(t, db, journal.Entry{
			ID:          fmt.Sprintf("j_%05d", i),
			WorkspaceID: "ws_test",
			MissionID:   "m1",
			TS:          base.Add(time.Duration(i) * time.Second),
			Type:        journal.EntryAssignmentCreate,
			ActorType:   journal.ActorAgent,
			Summary:     fmt.Sprintf("step %d", i),
		})
	}

	steps, err := Extract(context.Background(), db, "ws_test", "m1")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(steps) != total {
		t.Fatalf("steps = %d, want %d (pagination dropped rows?)", len(steps), total)
	}
	if steps[0].EntryID != "j_00000" || steps[total-1].EntryID != fmt.Sprintf("j_%05d", total-1) {
		t.Errorf("timeline order wrong: first=%s last=%s", steps[0].EntryID, steps[total-1].EntryID)
	}
	for i, s := range steps {
		if s.Index != i {
			t.Fatalf("Index not dense: steps[%d].Index = %d", i, s.Index)
		}
	}
}

// TestProjectEntry_Table walks every entry-type arm of projectEntry and
// asserts the projected step fields, including the drop (ok=false) arm.
func TestProjectEntry_Table(t *testing.T) {
	cases := []struct {
		name      string
		entry     journal.Entry
		wantOK    bool
		wantStep  TrajectoryStep
		checkTool bool
	}{
		{
			name:     "assignment created",
			entry:    journal.Entry{ID: "e1", Type: journal.EntryAssignmentCreate},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: true},
		},
		{
			name:     "assignment running",
			entry:    journal.Entry{ID: "e2", Type: journal.EntryAssignmentRun},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: true},
		},
		{
			name:     "assignment completed",
			entry:    journal.Entry{ID: "e3", Type: journal.EntryAssignmentDone},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: true},
		},
		{
			name:     "assignment failed",
			entry:    journal.Entry{ID: "e4", Type: journal.EntryAssignmentFail},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: false},
		},
		{
			name: "exec command nonzero exit",
			entry: journal.Entry{ID: "e5", Type: journal.EntryExecCommand, Payload: map[string]any{
				"command": "make test", "exit_code": float64(2), "elapsed_ms": float64(42),
			}},
			wantOK:    true,
			wantStep:  TrajectoryStep{ToolName: "make test", Success: false, ElapsedMs: 42},
			checkTool: true,
		},
		{
			name:      "exec command no payload defaults to success",
			entry:     journal.Entry{ID: "e6", Type: journal.EntryExecCommand},
			wantOK:    true,
			wantStep:  TrajectoryStep{Success: true},
			checkTool: true,
		},
		{
			name: "llm call",
			entry: journal.Entry{ID: "e7", Type: journal.EntryLLMCall, Payload: map[string]any{
				"model": "claude-x", "total_tokens": float64(1234), "duration_ms": float64(900),
			}},
			wantOK:    true,
			wantStep:  TrajectoryStep{ToolName: "claude-x", Success: true, TokenCost: 1234, ElapsedMs: 900},
			checkTool: true,
		},
		{
			name: "mission status change",
			entry: journal.Entry{ID: "e8", Type: journal.EntryMissionStatus, Payload: map[string]any{
				"to_status": "ACTIVE",
			}},
			wantOK:    true,
			wantStep:  TrajectoryStep{ToolName: "ACTIVE", Success: true},
			checkTool: true,
		},
		{
			name: "keeper allow",
			entry: journal.Entry{ID: "e9", Type: journal.EntryKeeperDecision, Payload: map[string]any{
				"credential_id": "cred-1", "decision": "allow",
			}},
			wantOK:    true,
			wantStep:  TrajectoryStep{ToolName: "cred-1", Success: true},
			checkTool: true,
		},
		{
			name: "keeper deny",
			entry: journal.Entry{ID: "e10", Type: journal.EntryKeeperDecision, Payload: map[string]any{
				"decision": "deny",
			}},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: false},
		},
		{
			name:     "guardrail input blocked",
			entry:    journal.Entry{ID: "e11", Type: journal.EntryGuardrailInput},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: false},
		},
		{
			name:     "guardrail output blocked",
			entry:    journal.Entry{ID: "e12", Type: journal.EntryGuardrailOutput},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: false},
		},
		{
			name:     "peer escalation kept",
			entry:    journal.Entry{ID: "e13", Type: journal.EntryPeerEscalation},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: true},
		},
		{
			name:     "budget exceeded is failure",
			entry:    journal.Entry{ID: "e14", Type: journal.EntryBudgetExceed},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: false},
		},
		{
			name:     "budget warning is failure",
			entry:    journal.Entry{ID: "e15", Type: journal.EntryBudgetWarning},
			wantOK:   true,
			wantStep: TrajectoryStep{Success: false},
		},
		{
			name:   "container metrics dropped",
			entry:  journal.Entry{ID: "e16", Type: journal.EntryContainerMetrics},
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			step, ok := projectEntry(tc.entry)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if step.EntryID != tc.entry.ID {
				t.Errorf("EntryID = %q, want %q", step.EntryID, tc.entry.ID)
			}
			if step.Success != tc.wantStep.Success {
				t.Errorf("Success = %v, want %v", step.Success, tc.wantStep.Success)
			}
			if tc.checkTool && step.ToolName != tc.wantStep.ToolName {
				t.Errorf("ToolName = %q, want %q", step.ToolName, tc.wantStep.ToolName)
			}
			if step.TokenCost != tc.wantStep.TokenCost {
				t.Errorf("TokenCost = %d, want %d", step.TokenCost, tc.wantStep.TokenCost)
			}
			if step.ElapsedMs != tc.wantStep.ElapsedMs {
				t.Errorf("ElapsedMs = %d, want %d", step.ElapsedMs, tc.wantStep.ElapsedMs)
			}
		})
	}
}

func TestPayloadString_KeyFallback(t *testing.T) {
	p := map[string]any{
		"empty":  "",
		"number": float64(7),
		"second": "hit",
	}
	if got := payloadString(p, "missing", "empty", "number", "second"); got != "hit" {
		t.Errorf("payloadString = %q, want \"hit\" (skip missing/empty/non-string)", got)
	}
	if got := payloadString(p, "missing", "empty", "number"); got != "" {
		t.Errorf("payloadString with no usable key = %q, want \"\"", got)
	}
	if got := payloadString(nil, "any"); got != "" {
		t.Errorf("payloadString(nil) = %q, want \"\"", got)
	}
}

func TestPayloadInt_NumericKinds(t *testing.T) {
	cases := []struct {
		name string
		p    map[string]any
		keys []string
		want int
	}{
		{"float64", map[string]any{"n": float64(12)}, []string{"n"}, 12},
		{"int", map[string]any{"n": int(34)}, []string{"n"}, 34},
		{"int64", map[string]any{"n": int64(56)}, []string{"n"}, 56},
		{"string value ignored", map[string]any{"n": "78"}, []string{"n"}, 0},
		{"missing key", map[string]any{}, []string{"n"}, 0},
		{"second key wins", map[string]any{"b": float64(9)}, []string{"a", "b"}, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := payloadInt(tc.p, tc.keys...); got != tc.want {
				t.Errorf("payloadInt = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPayloadExitOK_AllShapes(t *testing.T) {
	cases := []struct {
		name string
		p    map[string]any
		want bool
	}{
		{"nil payload", nil, true},
		{"missing exit_code", map[string]any{"other": 1}, true},
		{"float zero", map[string]any{"exit_code": float64(0)}, true},
		{"float nonzero", map[string]any{"exit_code": float64(1)}, false},
		{"int zero", map[string]any{"exit_code": int(0)}, true},
		{"int nonzero", map[string]any{"exit_code": int(127)}, false},
		{"int64 zero", map[string]any{"exit_code": int64(0)}, true},
		{"int64 nonzero", map[string]any{"exit_code": int64(2)}, false},
		{"bool true", map[string]any{"exit_code": true}, true},
		{"bool false", map[string]any{"exit_code": false}, false},
		{"unknown type permissive", map[string]any{"exit_code": "weird"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := payloadExitOK(tc.p); got != tc.want {
				t.Errorf("payloadExitOK(%v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestPayloadKeeperApproved_AllShapes(t *testing.T) {
	cases := []struct {
		name string
		p    map[string]any
		want bool
	}{
		{"nil payload", nil, false},
		{"allow", map[string]any{"decision": "allow"}, true},
		{"ALLOW", map[string]any{"decision": "ALLOW"}, true},
		{"Allow", map[string]any{"decision": "Allow"}, true},
		{"approved word", map[string]any{"decision": "approved"}, true},
		{"approve word", map[string]any{"decision": "approve"}, true},
		{"deny", map[string]any{"decision": "deny"}, false},
		{"approved bool true", map[string]any{"approved": true}, true},
		{"approved bool false", map[string]any{"approved": false}, false},
		{"deny with approved override", map[string]any{"decision": "deny", "approved": true}, true},
		{"nothing usable", map[string]any{"foo": "bar"}, false},
		{"decision non-string", map[string]any{"decision": 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := payloadKeeperApproved(tc.p); got != tc.want {
				t.Errorf("payloadKeeperApproved(%v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}
