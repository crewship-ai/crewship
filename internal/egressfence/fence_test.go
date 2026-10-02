package egressfence

import "testing"

func TestSpecValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"sidecar uid", Spec{AllowUIDs: []uint32{1002}}, false},
		{"empty allows nothing and fences nothing meaningful", Spec{}, true},
		{"root is never allowed", Spec{AllowUIDs: []uint32{1002, 0}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.spec.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestStateString(t *testing.T) {
	if got := (State{}).String(); got != "absent" {
		t.Fatalf("absent state = %q", got)
	}
	if got := (State{Present: true, Rules: 6, Valid: true}).String(); got != "present (6 rules)" {
		t.Fatalf("present state = %q", got)
	}
	if got := (State{Present: true, Rules: 6}).String(); got != "present but not valid (6 rules)" {
		t.Fatalf("invalid state = %q", got)
	}
}

// The per-rule marker is what lets Check tell this fence from a table that
// merely shares its name and rule count.
func TestSpecMarker(t *testing.T) {
	a := Spec{AllowUIDs: []uint32{1002}}
	b := Spec{AllowUIDs: []uint32{1003}}
	if a.marker(0) == a.marker(1) {
		t.Fatal("markers must differ per rule index")
	}
	if a.marker(0) == b.marker(0) {
		t.Fatal("markers must differ per allowed uid set")
	}
	if a.marker(2) != (Spec{AllowUIDs: []uint32{1002}}).marker(2) {
		t.Fatal("markers must be deterministic for the same spec")
	}
}
