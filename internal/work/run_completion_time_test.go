package work

import (
	"encoding/json"
	"testing"
)

func TestRunResultLegacyBytesAndTypedCompletionRoundTrip(t *testing.T) {
	zero := 0
	legacy := RunResult{ExitCode: &zero, Metadata: map[string]any{"captured_completed_at": json.Number("123")}}
	raw, err := json.Marshal(legacy)
	if err != nil || string(raw) != `{"exit_code":0,"metadata":{"captured_completed_at":123}}` {
		t.Fatalf("legacy bytes changed: %s %v", raw, err)
	}
	legacy.CompletedAt = "2026-10-19T10:00:42.125000000Z"
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RunResult
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.CompletedAt != legacy.CompletedAt {
		t.Fatalf("typed completion lost: %v", err)
	}
}
