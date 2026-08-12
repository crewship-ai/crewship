package api

import (
	"context"
	"testing"
)

// agent_ids exists so the console can draw an agent's real avatar and offer an
// "assigned to" facet. Both are keyed by id, and the wire format ships the ids
// in a SEPARATE array from the names — which is only safe while the two are
// index-aligned. These are the tests for that promise; without them a future
// change to either loader could silently pair a name with another agent's face.

func TestSplitAgentRefs_KeepsNamesAndIDsAligned(t *testing.T) {
	names, ids := splitAgentRefs([]agentRef{
		{ID: "ag_1", Name: "Alice"},
		{ID: "ag_2", Name: "Bob"},
		{ID: "ag_3", Name: "Carol"},
	})
	if len(names) != len(ids) {
		t.Fatalf("len(names)=%d len(ids)=%d — the two arrays must be parallel", len(names), len(ids))
	}
	for i, want := range []struct{ name, id string }{
		{"Alice", "ag_1"}, {"Bob", "ag_2"}, {"Carol", "ag_3"},
	} {
		if names[i] != want.name || ids[i] != want.id {
			t.Errorf("index %d = (%s, %s), want (%s, %s)", i, names[i], ids[i], want.name, want.id)
		}
	}
}

// Non-nil, not null. The console iterates both without a null check, and a JSON
// `null` where an array is declared is the shape that crashes a map().
func TestSplitAgentRefs_EmptyIsAnEmptyArrayNotNil(t *testing.T) {
	names, ids := splitAgentRefs(nil)
	if names == nil || ids == nil {
		t.Fatalf("names=%v ids=%v — both must be non-nil so they marshal as [] rather than null", names, ids)
	}
	if len(names) != 0 || len(ids) != 0 {
		t.Errorf("names=%v ids=%v, want both empty", names, ids)
	}
}

// The loader is the other half: it must return the id and the name of the SAME
// row, and it must skip agents that have been deleted (the join carries
// deleted_at IS NULL, and a credential still pointing at a tombstone must not
// report a face nobody can open).
func TestLoadAgentRefsBatch_ReturnsIDWithItsOwnName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('refs-crew', ?, 'C', 'refs-c')`, wsID)
	seedAgentRow(t, db, "refs-ag-b", wsID, "refs-crew", "Bravo", "refs-b", "AGENT")
	seedAgentRow(t, db, "refs-ag-a", wsID, "refs-crew", "Alpha", "refs-a", "AGENT")
	seedAgentRow(t, db, "refs-ag-gone", wsID, "refs-crew", "Ghost", "refs-g", "AGENT")
	execOrFatal(t, db, `UPDATE agents SET deleted_at = datetime('now') WHERE id = 'refs-ag-gone'`)
	seedCredentialEnc(t, db, wsID, userID, "refs-cred", "refs-name", "secret")
	for _, a := range []string{"refs-ag-a", "refs-ag-b", "refs-ag-gone"} {
		execOrFatal(t, db, `INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
			VALUES ('ac-'||?, ?, 'refs-cred', 'X', 0, datetime('now'))`, a, a)
	}

	h := NewCredentialHandler(db, newTestLogger())
	got := h.loadAgentRefsBatch(context.Background(), []string{"refs-cred"})["refs-cred"]

	// Ordered by name, deleted agent absent.
	want := []agentRef{{ID: "refs-ag-a", Name: "Alpha"}, {ID: "refs-ag-b", Name: "Bravo"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
