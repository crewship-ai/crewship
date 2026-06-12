package main

import (
	"strings"
	"testing"
)

func TestCheckEgressTargets_NoTargetsNoHTTPSteps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		def  map[string]interface{}
	}{
		{"no keys at all", map[string]interface{}{}},
		{"empty target list", map[string]interface{}{"egress_targets": []interface{}{}}},
		{"steps without http", map[string]interface{}{
			"steps": []interface{}{map[string]interface{}{"type": "agent"}},
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := checkEgressTargets(tc.def)
			if len(got) != 1 {
				t.Fatalf("want 1 check, got %d: %+v", len(got), got)
			}
			if got[0].Name != "egress_allowlist" || got[0].Level != doctorOK {
				t.Errorf("got %+v; want egress_allowlist OK", got[0])
			}
			if !strings.Contains(got[0].Message, "allowlist not required") {
				t.Errorf("Message: got %q", got[0].Message)
			}
		})
	}
}

func TestCheckEgressTargets_HTTPStepsButEmptyList(t *testing.T) {
	t.Parallel()

	def := map[string]interface{}{
		"steps": []interface{}{
			map[string]interface{}{"type": "agent"},
			map[string]interface{}{"type": "http"},
		},
	}
	got := checkEgressTargets(def)
	if len(got) != 1 {
		t.Fatalf("want 1 check, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Level != doctorWarn {
		t.Errorf("Level: got %q want WARN", c.Level)
	}
	if !strings.Contains(c.Message, "egress_targets is empty") {
		t.Errorf("Message: got %q", c.Message)
	}
	if !strings.Contains(c.Hint, "egress_targets") {
		t.Errorf("Hint: got %q", c.Hint)
	}
}

func TestCheckEgressTargets_Wildcards(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"*", "*.*", ""} {
		host := host
		t.Run("wildcard "+host, func(t *testing.T) {
			t.Parallel()
			def := map[string]interface{}{
				"egress_targets": []interface{}{host},
			}
			got := checkEgressTargets(def)
			if len(got) != 1 {
				t.Fatalf("want 1 issue, got %d: %+v", len(got), got)
			}
			if got[0].Level != doctorWarn {
				t.Errorf("Level: got %q want WARN", got[0].Level)
			}
			if !strings.Contains(got[0].Message, "wildcard") {
				t.Errorf("Message: got %q", got[0].Message)
			}
		})
	}
}

func TestCheckEgressTargets_Loopback(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"localhost:8080", "127.0.0.1", "127.0.0.2"} {
		host := host
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			def := map[string]interface{}{
				"egress_targets": []interface{}{host},
			}
			got := checkEgressTargets(def)
			if len(got) != 1 {
				t.Fatalf("want 1 issue, got %d: %+v", len(got), got)
			}
			if got[0].Level != doctorWarn {
				t.Errorf("Level: got %q want WARN", got[0].Level)
			}
			if !strings.Contains(got[0].Message, "loopback") {
				t.Errorf("Message: got %q", got[0].Message)
			}
		})
	}
}

func TestCheckEgressTargets_CollectsAllIssues(t *testing.T) {
	t.Parallel()

	// One wildcard AND one loopback in the same allowlist → both surfaced
	// in a single pass.
	def := map[string]interface{}{
		"egress_targets": []interface{}{"*", "localhost", "api.example.com"},
	}
	got := checkEgressTargets(def)
	if len(got) != 2 {
		t.Fatalf("want 2 issues, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Message, "wildcard") {
		t.Errorf("first issue: got %q", got[0].Message)
	}
	if !strings.Contains(got[1].Message, "loopback") {
		t.Errorf("second issue: got %q", got[1].Message)
	}
}

func TestCheckEgressTargets_CleanList(t *testing.T) {
	t.Parallel()

	def := map[string]interface{}{
		"egress_targets": []interface{}{"api.example.com", "hooks.slack.com"},
	}
	got := checkEgressTargets(def)
	if len(got) != 1 {
		t.Fatalf("want 1 check, got %d: %+v", len(got), got)
	}
	c := got[0]
	if c.Level != doctorOK {
		t.Errorf("Level: got %q want OK", c.Level)
	}
	if !strings.Contains(c.Message, "2 target(s) declared") {
		t.Errorf("Message: got %q", c.Message)
	}
}

func TestHasHTTPStep(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		def  map[string]interface{}
		want bool
	}{
		{"no steps key", map[string]interface{}{}, false},
		{"steps wrong type", map[string]interface{}{"steps": 42}, false},
		{"non-map step skipped", map[string]interface{}{
			"steps": []interface{}{"oops"},
		}, false},
		{"agent step only", map[string]interface{}{
			"steps": []interface{}{map[string]interface{}{"type": "agent"}},
		}, false},
		{"http step present", map[string]interface{}{
			"steps": []interface{}{
				map[string]interface{}{"type": "agent"},
				map[string]interface{}{"type": "http"},
			},
		}, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasHTTPStep(tc.def); got != tc.want {
				t.Errorf("hasHTTPStep: got %v want %v", got, tc.want)
			}
		})
	}
}
