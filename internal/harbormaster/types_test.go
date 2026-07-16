package harbormaster

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRequest_JSONWireFormat pins the snake_case wire contract of
// GET /api/v1/approvals rows. Both first-party consumers (the CLI's
// struct tags and the dashboard's zod approvalRowSchema) require these
// exact keys; the struct used to marshal with Go-cased names, which
// silently blanked crew_id/created_at/decided_by on both clients.
func TestRequest_JSONWireFormat(t *testing.T) {
	b, err := json.Marshal(Request{ID: "r1", CrewID: "c1", Kind: KindToolCall, TimeoutSecs: 60})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, key := range []string{`"id"`, `"workspace_id"`, `"crew_id"`, `"agent_id"`, `"mission_id"`,
		`"requested_by"`, `"kind"`, `"reason"`, `"status"`, `"decided_by"`, `"decided_at"`,
		`"comment"`, `"timeout_at"`, `"created_at"`} {
		if !strings.Contains(got, key) {
			t.Errorf("wire format missing %s: %s", key, got)
		}
	}
	for _, gone := range []string{"TimeoutSecs", `"ID"`, `"CrewID"`, `"CreatedAt"`} {
		if strings.Contains(got, gone) {
			t.Errorf("wire format must not contain %s: %s", gone, got)
		}
	}
}

// TestMode_String pins the human-readable rendering used in logs and
// journal entries. Adding a new Mode constant must consciously update
// this test.
func TestMode_String(t *testing.T) {
	tests := []struct {
		mode Mode
		want string
	}{
		{ModeNone, "none"},
		{ModeAsync, "async"},
		{ModeSync, "sync"},
		{Mode(99), "none"}, // unknown falls through to default
		{Mode(-1), "none"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("Mode(%d).String() = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}
