package quota

import (
	"errors"
	"strings"
	"testing"
)

func TestPortableVolumeContract(t *testing.T) {
	for _, tc := range []struct {
		name, service, volume, mount string
		enabled                      bool
		generation, bytes            int64
		allowed                      bool
	}{
		{"trusted legacy", "", "", "", false, 0, 0, true},
		{"disabled with quota", "database", "data", "/data", false, 0, MinBytes, false},
		{"disabled with generation", "database", "data", "/data", false, 1, 0, false},
		{"first implicit generation", "database", "data", "/data", true, 0, MinBytes, true},
		{"maximum size", "database", "data", "/data/subdirectory", true, 5, MaxBytes, true},
		{"unreserved sibling", "database", "data", "/tmp-data", true, 1, MinBytes, true},
		{"negative generation", "database", "data", "/data", true, -1, MinBytes, false},
		{"path service", "../database", "data", "/data", true, 1, MinBytes, false},
		{"path volume", "database", "../data", "/data", true, 1, MinBytes, false},
		{"overlong name", strings.Repeat("a", 129), "data", "/data", true, 1, MinBytes, false},
		{"too small", "database", "data", "/data", true, 1, MinBytes - 1, false},
		{"too large", "database", "data", "/data", true, 1, MaxBytes + 1, false},
		{"unaligned", "database", "data", "/data", true, 1, MinBytes + 1, false},
		{"relative mount", "database", "data", "data", true, 1, MinBytes, false},
		{"root mount", "database", "data", "/", true, 1, MinBytes, false},
		{"unclean mount", "database", "data", "/data/../other", true, 1, MinBytes, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateVolume(tc.enabled, tc.service, tc.volume, tc.mount, tc.generation, tc.bytes)
			if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrDenied) {
				t.Fatalf("volume allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
	for _, root := range []string{"/tmp", "/run", "/var/run", "/dev", "/proc", "/sys"} {
		for _, mount := range []string{root, root + "/data"} {
			if err := ValidateVolume(true, "database", "data", mount, 1, MinBytes); !errors.Is(err, ErrDenied) {
				t.Errorf("reserved mount admitted %s: %v", mount, err)
			}
		}
	}
}
