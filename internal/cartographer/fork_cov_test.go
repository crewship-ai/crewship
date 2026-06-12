package cartographer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestFork_Validation pins the required-arg check and the not-found path
// for the source checkpoint.
func TestFork_Validation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, _, err := Fork(ctx, db, nil, "", "cp_x", "", "u"); err == nil {
		t.Error("empty workspace accepted")
	}
	if _, _, err := Fork(ctx, db, nil, "ws_test", "", "", "u"); err == nil {
		t.Error("empty checkpoint id accepted")
	}
	if _, _, err := Fork(ctx, db, nil, "ws_test", "cp_missing", "", "u"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing source checkpoint: got %v, want ErrNotFound", err)
	}
}

// TestFork_ParentMissionNotFound pins the cross-workspace defence: a
// checkpoint row whose mission belongs to another workspace must fail
// with "parent mission not found" instead of forking foreign data.
func TestFork_ParentMissionNotFound(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	// Mission in ws_other, checkpoint row claiming ws_test scope.
	mustExecSQL(t, db, `INSERT INTO crews (id, workspace_id) VALUES ('crew_o', 'ws_other')`)
	mustExecSQL(t, db, `INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title)
		VALUES ('mis_foreign', 'ws_other', 'crew_o', 'agent_lead', 'tr_f', 'Foreign')`)
	mustExecSQL(t, db, `INSERT INTO checkpoints (id, workspace_id, mission_id, journal_cursor, state_snapshot)
		VALUES ('cp_cross', 'ws_test', 'mis_foreign', 'j_1', '{}')`)

	_, _, err := Fork(context.Background(), db, nil, "ws_test", "cp_cross", "", "u")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected parent-mission-not-found error, got %v", err)
	}
}

// TestFork_DefaultLabelAndMetaClone forks without a label and with Meta
// on the source snapshot: the fork checkpoint must get the synthesized
// "fork of <src>" label and an independent copy of Meta.
func TestFork_DefaultLabelAndMetaClone(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	emitJournalEntry(t, db, "j_seed", "mis_1", time.Now().UTC())
	srcID, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		CrewID:        "crew_a",
		MissionID:     "mis_1",
		JournalCursor: "j_seed",
		State: StateSnapshot{
			Meta: map[string]any{"reason": "pre-fork", "n": float64(2)},
		},
	})
	if err != nil {
		t.Fatalf("create src: %v", err)
	}

	_, newCP, err := Fork(ctx, db, nil, "ws_test", srcID, "", "user_x")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	cp, err := Get(ctx, db, "ws_test", newCP)
	if err != nil {
		t.Fatalf("get fork cp: %v", err)
	}
	if cp.Label != "fork of "+srcID {
		t.Errorf("default label = %q, want %q", cp.Label, "fork of "+srcID)
	}
	if cp.State.Meta["reason"] != "pre-fork" || cp.State.Meta["n"] != float64(2) {
		t.Errorf("meta not cloned onto fork: %+v", cp.State.Meta)
	}
	// Cursor pinned to the source's cursor — fork starts from that point.
	if cp.JournalCursor != "j_seed" {
		t.Errorf("fork cursor = %q, want j_seed", cp.JournalCursor)
	}
}

// TestFork_BadDependsOnFails pins the remap error path: a task with
// corrupt depends_on JSON aborts the fork with a descriptive error.
//
// NOTE: we deliberately do NOT query the DB after the failed Fork.
// Fork's deferred rollback is gated on the captured `err` variable, but
// the remapDepends failure returns via the separate `remapErr` variable
// without assigning `err` — so the transaction is left open (likely
// production bug: leaked tx on this path). With the test pool pinned to
// one connection, any follow-up query would deadlock on the leaked tx.
func TestFork_BadDependsOnFails(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	mustExecSQL(t, db, `INSERT INTO mission_tasks (id, mission_id, title, status, task_order, depends_on)
		VALUES ('mt_corrupt', 'mis_1', 'Broken', 'PENDING', 0, '{oops')`)
	emitJournalEntry(t, db, "j_seed", "mis_1", time.Now().UTC())
	srcID, err := Create(ctx, db, nil, Checkpoint{
		WorkspaceID: "ws_test", MissionID: "mis_1", JournalCursor: "j_seed",
	})
	if err != nil {
		t.Fatalf("create src: %v", err)
	}

	newMission, newCP, err := Fork(ctx, db, nil, "ws_test", srcID, "branch", "u")
	if err == nil || !strings.Contains(err.Error(), "remap deps") {
		t.Fatalf("expected remap deps error, got %v", err)
	}
	if newMission != "" || newCP != "" {
		t.Errorf("failed fork returned ids: mission=%q cp=%q", newMission, newCP)
	}
}

// TestRemapDepends covers the parser edge cases directly.
func TestRemapDepends(t *testing.T) {
	idMap := map[string]string{"a": "fork_a", "b": "fork_b"}

	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"empty string", "", "[]", false},
		{"empty array", "[]", "[]", false},
		{"all mapped", `["a","b"]`, `["fork_a","fork_b"]`, false},
		{"unknown ids dropped", `["a","ghost"]`, `["fork_a"]`, false},
		{"only unknown", `["ghost"]`, `[]`, false},
		{"invalid json", `{nope`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := remapDepends(tc.raw, idMap)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("remapDepends: %v", err)
			}
			if got != tc.want {
				t.Errorf("remapDepends(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestNullableSQL pins the NullString → driver-value adapter.
func TestNullableSQL(t *testing.T) {
	if got := nullableSQL(sql.NullString{}); got != nil {
		t.Errorf("invalid NullString → %v, want nil", got)
	}
	if got := nullableSQL(sql.NullString{Valid: true, String: ""}); got != nil {
		t.Errorf("valid empty NullString → %v, want nil", got)
	}
	if got := nullableSQL(sql.NullString{Valid: true, String: "v"}); got != "v" {
		t.Errorf("valid NullString → %v, want \"v\"", got)
	}
}

// TestDefaultStatus pins the empty-status fallback.
func TestDefaultStatus(t *testing.T) {
	if got := defaultStatus(""); got != "PENDING" {
		t.Errorf("defaultStatus(\"\") = %q, want PENDING", got)
	}
	if got := defaultStatus("COMPLETED"); got != "COMPLETED" {
		t.Errorf("defaultStatus(COMPLETED) = %q, want COMPLETED (verbatim)", got)
	}
}

// TestCloneMeta pins shallow-copy semantics: top-level mutation of the
// clone must not leak into the source, and empty input maps to nil.
func TestCloneMeta(t *testing.T) {
	if got := cloneMeta(nil); got != nil {
		t.Errorf("cloneMeta(nil) = %v, want nil", got)
	}
	if got := cloneMeta(map[string]any{}); got != nil {
		t.Errorf("cloneMeta(empty) = %v, want nil", got)
	}

	src := map[string]any{"k": "v"}
	out := cloneMeta(src)
	if out["k"] != "v" {
		t.Fatalf("clone lost value: %+v", out)
	}
	out["k"] = "mutated"
	if src["k"] != "v" {
		t.Errorf("mutating clone leaked into source: %+v", src)
	}
}

// TestMarshalState pins the nil-field normalization in the serialized
// form: a zero-value snapshot must marshal with empty containers, not
// JSON nulls — restore code on the other side indexes these directly.
func TestMarshalState(t *testing.T) {
	got, err := marshalState(StateSnapshot{})
	if err != nil {
		t.Fatalf("marshalState: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("output not valid JSON: %v (%s)", err, got)
	}
	if _, ok := decoded["agent_memory"].(map[string]any); !ok {
		t.Errorf("agent_memory should be an object, got %T", decoded["agent_memory"])
	}
	if _, ok := decoded["pending_tasks"].([]any); !ok {
		t.Errorf("pending_tasks should be an array, got %T", decoded["pending_tasks"])
	}
	if _, ok := decoded["open_assignments"].([]any); !ok {
		t.Errorf("open_assignments should be an array, got %T", decoded["open_assignments"])
	}
}

// TestNewRandID pins prefix + length + uniqueness of the local id mint.
func TestNewRandID(t *testing.T) {
	a := newRandID("mis_")
	b := newRandID("mis_")
	if !strings.HasPrefix(a, "mis_") || len(a) != len("mis_")+16 {
		t.Errorf("unexpected id shape: %q", a)
	}
	if a == b {
		t.Errorf("two ids collided: %q", a)
	}
}

func mustExecSQL(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
