package access

import (
	"encoding/json"
	"testing"
)

func TestPresetMetadataAdministrativePolicyOnly(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "list"}, Right{"project", "p1", "read"})
	insertPreset(t, s, "m1", "project", "p1", "owner")
	admin, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(admin)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	var choices []PresetSelection
	if err := json.Unmarshal(wire["preset_selections"], &choices); err != nil || len(choices) != 1 || choices[0].State != "selected" || choices[0].SelectedBy == nil || *choices[0].SelectedBy != "owner" {
		t.Fatalf("administrative policy lacks server-computed selection provenance: %s (%v)", b, err)
	}
	own, err := s.OwnPolicy(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(own)
	if err != nil {
		t.Fatal(err)
	}
	wire = map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["preset_selections"]; ok {
		t.Fatal("OwnPolicy leaks administrator-only selection provenance")
	}
	// Identical bits without saved intent must remain Custom, not fabricated.
	if _, err := s.DB.Exec(`DELETE FROM access_preset_selections`); err != nil {
		t.Fatal(err)
	}
	admin, err = s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	b, err = json.Marshal(admin)
	if err != nil {
		t.Fatal(err)
	}
	wire = map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["preset_selections"]) != "[]" {
		t.Fatalf("empty administrative selections must be [], got%s", b)
	}
}
