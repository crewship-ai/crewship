package main

import (
	"strings"
	"testing"
)

// Each inherited grant_source is stopped at its own layer; the message must
// name that layer, not the crew link for everything.
func TestInheritedUnassignError(t *testing.T) {
	cases := []struct {
		source, want, notWant string
	}{
		{"crew", "--crews", "unbind"},
		{"binding", "credential unbind", "--crews"},
		{"other", "layer that grants it", "--crews"},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			msg := inheritedUnassignError("FOO", "ops-agent", tc.source).Error()
			if !strings.Contains(msg, tc.want) || strings.Contains(msg, tc.notWant) {
				t.Fatalf("message for %s = %q", tc.source, msg)
			}
			if !strings.Contains(msg, "grant_source="+tc.source) {
				t.Fatalf("message does not name the source: %q", msg)
			}
		})
	}
}
