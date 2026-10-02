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
	if got := (State{Present: true, Rules: 6}).String(); got != "present (6 rules)" {
		t.Fatalf("present state = %q", got)
	}
}
