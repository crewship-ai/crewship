package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeRequestPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		// happy path
		{"plain", "config/x.toml", "config/x.toml", true},
		{"single_segment", "x.txt", "x.txt", true},
		{"dot_collapsed", "./a/./b", "a/b", true},
		// escapes
		{"empty", "", "", false},
		{"absolute", "/etc/passwd", "", false},
		{"dotdot_only", "..", "", false},
		{"dotdot_prefix", "../foo", "", false},
		{"dotdot_middle", "a/../../etc", "", false},
		{"dotdot_after_clean", "a/b/../../..", "", false},
		// double-encoded variants must NOT slip through (F-005)
		{"double_encoded_dotdot_lower", "%2e%2e/etc", "", false},
		{"double_encoded_dotdot_upper", "%2E%2E/etc", "", false},
		{"double_encoded_slash", "..%2f..%2fetc", "", false},
		{"double_encoded_backslash", "..%5c..%5cetc", "", false},
		// NUL injection
		{"nul_byte", "config\x00.toml", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeRequestPath(tc.in)
			assert.Equal(t, tc.ok, ok)
			if ok {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
