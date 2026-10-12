package memory

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestGuaranteedDispatcherRequiresCallerSuppliedOperationID(t *testing.T) {
	f := newMutateFixture(t)
	d := NewDispatcher(AgentContext{AgentID: "alice", WorkspaceID: "ws_test", AgentMemoryDir: f.dir}, WithMutationLedger(f.db, f.blobRoot), WithMutationProfile(ProfileGuaranteed))
	raw := json.RawMessage(`{"tier":"AGENT","mode":"append","content":"a durable fact\n"}`)
	result, err := d.Dispatch(t.Context(), ToolCall{Name: "memory.write", Args: raw})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("guaranteed write minted an operation id instead of requiring a retry identity: %+v", result)
	}
	var count int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_mutations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid guaranteed write persisted: %d %v", count, err)
	}
	raw = json.RawMessage(`{"tier":"AGENT","mode":"append","content":"a durable fact\n","operation_id":"caller-stable-id"}`)
	for range 2 {
		result, err = d.Dispatch(t.Context(), ToolCall{Name: "memory.write", Args: raw})
		if err != nil || result.IsError {
			t.Fatalf("explicit retry identity rejected: %+v %v", result, err)
		}
	}
	body, err := readRegularNoFollow(filepath.Join(f.dir, "AGENT.md"))
	if err != nil || string(body) != "a durable fact\n" {
		t.Fatalf("idempotent retry duplicated content: %q %v", body, err)
	}
}
