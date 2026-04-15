package backup

import (
	"errors"
	"testing"
)

func TestIsCompatible(t *testing.T) {
	tests := []struct {
		name    string
		written int
		want    bool
	}{
		{"current version", FormatVersion, true},
		{"min supported", MinSupportedFormatVersion, true},
		{"too new", FormatVersion + 1, false},
		{"too old", MinSupportedFormatVersion - 1, false},
		{"zero", 0, false},
		{"negative", -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCompatible(tt.written); got != tt.want {
				t.Errorf("IsCompatible(%d) = %v, want %v", tt.written, got, tt.want)
			}
		})
	}
}

func TestCompatibilityReason(t *testing.T) {
	if err := CompatibilityReason(FormatVersion); err != nil {
		t.Errorf("current version should be compatible, got %v", err)
	}
	if err := CompatibilityReason(FormatVersion + 1); !errors.Is(err, ErrFormatTooNew) {
		t.Errorf("future version should return ErrFormatTooNew, got %v", err)
	}
	if err := CompatibilityReason(MinSupportedFormatVersion - 1); !errors.Is(err, ErrFormatTooOld) {
		t.Errorf("ancient version should return ErrFormatTooOld, got %v", err)
	}
}

func TestFormatVersionInvariants(t *testing.T) {
	if FormatVersion < 1 {
		t.Errorf("FormatVersion must be >= 1, got %d", FormatVersion)
	}
	if MinSupportedFormatVersion < 1 {
		t.Errorf("MinSupportedFormatVersion must be >= 1, got %d", MinSupportedFormatVersion)
	}
	if MinSupportedFormatVersion > FormatVersion {
		t.Errorf("MinSupportedFormatVersion (%d) must not exceed FormatVersion (%d)",
			MinSupportedFormatVersion, FormatVersion)
	}
	// N-2 policy: never support more than 3 versions back.
	if FormatVersion-MinSupportedFormatVersion > 2 {
		t.Errorf("reader must not support more than 3 versions back; got %d..%d",
			MinSupportedFormatVersion, FormatVersion)
	}
}
