package main

import (
	"strings"
	"testing"
)

// The reverse gate's directory exclusions are genre-based, not convenience.
// These tests pin the classification the 2026-09-28 repository-clarity work
// established: current contracts are checked, proposals and history are not,
// and a directory whose name merely starts like an excluded one is still
// checked.
func TestReverseGateApplies(t *testing.T) {
	cases := []struct {
		path string
		appl bool
		why  string
	}{
		{"docs/specs/pages.md", true, "current contract — moved out of docs/prd to be checked"},
		{"docs/specs/response-shape-contract.md", true, "current API contract"},
		{"docs/runbooks/ci-cd-implementation-2026-09-11.md", true, "operational runbook names real commands"},
		{"docs/guides/inbox.mdx", true, "user-facing guide"},
		{"docs/prd/keeper-configuration.md", false, "proposal naming a flag that does not exist yet"},
		{"docs/prd/sub/dir/nested.md", false, "exclusion is a prefix, at any depth"},
		{"docs/archive/pages-apps.md", false, "history — describes superseded designs"},
		{"docs/archive/sub/deep.md", false, "exclusion is a prefix, at any depth"},
		{"docs/prd-legacy/foo.md", true, "similar prefix, different directory — must not be excluded"},
		{"docs/archived-notes/foo.md", true, "similar prefix, different directory — must not be excluded"},
		{"CONTRIBUTING.md", true, "root documentation"},
	}
	for _, tc := range cases {
		if got := reverseGateApplies(tc.path); got != tc.appl {
			t.Errorf("reverseGateApplies(%q) = %v, want %v (%s)", tc.path, got, tc.appl, tc.why)
		}
	}
}

// The exclusion list itself must stay exact: a typo'd or broadened prefix
// silently stops checking documents that claim to be current.
func TestReverseGateExcludedPrefixesExact(t *testing.T) {
	want := []string{"docs/prd/", "docs/archive/"}
	if len(reverseGateExcludedPrefixes) != len(want) {
		t.Fatalf("reverseGateExcludedPrefixes = %v, want exactly %v", reverseGateExcludedPrefixes, want)
	}
	for i, prefix := range want {
		if reverseGateExcludedPrefixes[i] != prefix {
			t.Errorf("reverseGateExcludedPrefixes[%d] = %q, want %q", i, reverseGateExcludedPrefixes[i], prefix)
		}
		if !strings.HasSuffix(prefix, "/") {
			t.Errorf("prefix %q must end in a slash so sibling directories are not excluded", prefix)
		}
	}
}
