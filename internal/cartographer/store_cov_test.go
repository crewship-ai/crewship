package cartographer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestCreate_Validation pins the three required-field checks: missing
// workspace, mission, or journal cursor must be rejected before any SQL
// runs.
func TestCreate_Validation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	cases := []struct {
		name string
		cp   Checkpoint
		want string
	}{
		{"missing workspace", Checkpoint{MissionID: "mis_1", JournalCursor: "j"}, "workspace_id"},
		{"missing mission", Checkpoint{WorkspaceID: "ws_test", JournalCursor: "j"}, "mission_id"},
		{"missing cursor", Checkpoint{WorkspaceID: "ws_test", MissionID: "mis_1"}, "journal_cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(ctx, db, nil, tc.cp)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// TestCreate_InsertErrorOnUnknownMission proves the FK constraint surfaces
// as a wrapped insert error rather than a silent success — checkpoints
// must never point at missions that don't exist.
func TestCreate_InsertErrorOnUnknownMission(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	_, err := Create(context.Background(), db, nil, Checkpoint{
		WorkspaceID:   "ws_test",
		MissionID:     "mis_ghost",
		JournalCursor: "j_x",
	})
	if err == nil {
		t.Fatal("expected FK violation error")
	}
	if !strings.Contains(err.Error(), "insert") {
		t.Errorf("error should be wrapped as insert failure, got %v", err)
	}
}

// TestGet_Validation pins the required-args check and the ErrNotFound
// sentinel for unknown ids.
func TestGet_Validation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := Get(ctx, db, "", "cp_x"); err == nil {
		t.Error("empty workspace accepted")
	}
	if _, err := Get(ctx, db, "ws_test", ""); err == nil {
		t.Error("empty id accepted")
	}
	_, err := Get(ctx, db, "ws_test", "cp_missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: got %v, want ErrNotFound", err)
	}
}

// TestGet_CorruptStateJSON pins the unmarshal error path: a row whose
// state_snapshot is not valid JSON must produce a loud error, not a
// zero-value snapshot.
func TestGet_CorruptStateJSON(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO checkpoints (id, workspace_id, mission_id, journal_cursor, state_snapshot)
		VALUES ('cp_bad', 'ws_test', 'mis_1', 'j_1', '{not json')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := Get(context.Background(), db, "ws_test", "cp_bad")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	if !strings.Contains(err.Error(), "unmarshal state") {
		t.Errorf("error should mention unmarshal, got %v", err)
	}
}

// TestGet_EmptyStateNormalized pins scanCheckpoint's nil-field defaults:
// an empty state_snapshot string yields non-nil maps/slices so callers
// can index without nil checks.
func TestGet_EmptyStateNormalized(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO checkpoints (id, workspace_id, mission_id, journal_cursor, state_snapshot)
		VALUES ('cp_empty', 'ws_test', 'mis_1', 'j_1', '')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cp, err := Get(context.Background(), db, "ws_test", "cp_empty")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cp.State.AgentMemory == nil {
		t.Error("AgentMemory not normalized to empty map")
	}
	if cp.State.PendingTasks == nil {
		t.Error("PendingTasks not normalized to empty slice")
	}
	if cp.State.OpenAssignments == nil {
		t.Error("OpenAssignments not normalized to empty slice")
	}
}

// TestList_Validation pins both required args and the default limit.
func TestList_Validation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := List(ctx, db, "", "mis_1", 10); err == nil {
		t.Error("empty workspace accepted")
	}
	if _, err := List(ctx, db, "ws_test", "", 10); err == nil {
		t.Error("empty mission accepted")
	}

	// limit <= 0 falls back to 50 — a couple of rows must come back fine.
	for i := 0; i < 2; i++ {
		if _, err := Create(ctx, db, nil, Checkpoint{
			WorkspaceID: "ws_test", MissionID: "mis_1", JournalCursor: "j_1",
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	got, err := List(ctx, db, "ws_test", "mis_1", 0)
	if err != nil {
		t.Fatalf("list with limit 0: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("default-limit list returned %d, want 2", len(got))
	}
}

// TestList_ScanErrorPropagates pins the row-level error path: one corrupt
// state row poisons the whole List call — better loud than a silently
// truncated history.
func TestList_ScanErrorPropagates(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if _, err := db.Exec(`INSERT INTO checkpoints (id, workspace_id, mission_id, journal_cursor, state_snapshot)
		VALUES ('cp_bad', 'ws_test', 'mis_1', 'j_1', 'not-json')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := List(context.Background(), db, "ws_test", "mis_1", 10); err == nil {
		t.Error("expected error from corrupt state row")
	}
}

// TestList_QueryError pins the SQL failure path (missing table → wrapped
// "list" error).
func TestList_QueryError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE checkpoints`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	_, err := List(context.Background(), db, "ws_test", "mis_1", 10)
	if err == nil || !strings.Contains(err.Error(), "list") {
		t.Errorf("expected wrapped list error, got %v", err)
	}
}

// TestDelete_Validation pins the arg checks and the ErrNotFound sentinel
// when zero rows match.
func TestDelete_Validation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := Delete(ctx, db, "", "cp_x"); err == nil {
		t.Error("empty workspace accepted")
	}
	if err := Delete(ctx, db, "ws_test", ""); err == nil {
		t.Error("empty id accepted")
	}
	if err := Delete(ctx, db, "ws_test", "cp_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row: got %v, want ErrNotFound", err)
	}
}

// TestDelete_QueryError pins the exec failure path.
func TestDelete_QueryError(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE checkpoints`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	err := Delete(context.Background(), db, "ws_test", "cp_x")
	if err == nil || !strings.Contains(err.Error(), "delete") {
		t.Errorf("expected wrapped delete error, got %v", err)
	}
}

// TestParseTS covers every accepted layout plus the unparseable error.
func TestParseTS(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		want    time.Time
	}{
		{"2026-06-12T10:11:12.123456789Z", false, time.Date(2026, 6, 12, 10, 11, 12, 123456789, time.UTC)},
		{"2026-06-12T10:11:12Z", false, time.Date(2026, 6, 12, 10, 11, 12, 0, time.UTC)},
		{"2026-06-12T10:11:12.500Z", false, time.Date(2026, 6, 12, 10, 11, 12, 500000000, time.UTC)},
		{"2026-06-12 10:11:12", false, time.Date(2026, 6, 12, 10, 11, 12, 0, time.UTC)},
		{"yesterday-ish", true, time.Time{}},
		{"", true, time.Time{}},
	}
	for _, tc := range cases {
		got, err := parseTS(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseTS(%q): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTS(%q): %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("parseTS(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestNullable pins the empty-string → NULL adapter.
func TestNullable(t *testing.T) {
	if got := nullable(""); got != nil {
		t.Errorf("nullable(\"\") = %v, want nil", got)
	}
	if got := nullable("x"); got != "x" {
		t.Errorf("nullable(\"x\") = %v, want \"x\"", got)
	}
}
