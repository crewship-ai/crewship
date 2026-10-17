package access

import "testing"

func TestPresetSelectionSchemaStartsEmptyWithoutChangingAuthority(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "read"})
	var tables int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='access_preset_selections'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("informational preset selection table missing: got %d, want 1", tables)
	}
	var rows int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM access_preset_selections`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("existing independent grant inferred a preset: rows=%d err=%v", rows, err)
	}
	if err := s.Check(t.Context(), "h1", "w", Right{"project", "p1", "read"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(t.Context(), "h1", "w", Right{"project", "p1", "manage"}); err == nil {
		t.Fatal("schema migration widened existing resource authority")
	}
}
