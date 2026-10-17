package access

import (
	"errors"
	"reflect"
	"testing"
)

// RAISE(IGNORE) is successful SQL with zero inserted rows. The recorder must
// refuse success rather than silently lose the final informational selection.
func TestPresetRecordIgnoredInsertReturnsConflict(t *testing.T) {
	s := fixture(t)
	m := policy(t, s, "h1", Right{"project", "p1", "list"}, Right{"project", "p1", "read"}, Right{"project", "p2", "read"})
	before, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`CREATE TRIGGER ignore_preset_selection BEFORE INSERT ON access_preset_selections BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	req := PresetRequest{MembershipID: m.ID, Revision: m.Revision, Resource: PresetResource{Kind: "project", ID: "p1"}, PresetID: "project-levels", Version: 1, Level: "view"}
	if err := s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", m, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("ignored selection INSERT reported success/non-conflict: %v", err)
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM access_preset_selections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unexpected selection after ignored INSERT: %d %v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed recorder changed canonical policy: before=%+v after=%+v err=%v", before, after, err)
	}
}
