package main

import "testing"

func TestCanonicalPathIsSafe(t *testing.T) {
	// allowedRoot anchors all containment checks; tests verify both
	// the "outside the root" rejection and the "inside the root with
	// traversal markers" rejection. allowedRoot="" preserves the
	// permissive legacy behaviour for non-server callers.
	const root = "/var/lib/crewship/memory"
	cases := []struct {
		path        string
		allowedRoot string
		want        bool
	}{
		// Legacy mode (no root) — non-empty + no ".." passes.
		{"", "", false},
		{"   ", "", false},
		{"AGENT.md", "", true},
		{"../etc/passwd", "", false},
		{"a/../b", "", false},
		// Confined mode — must land inside allowedRoot.
		{root + "/topics/crew_a/learned-2026-05-17.md", root, true},
		{"/etc/passwd", root, false},
		{root + "-evil/sneaky.md", root, false}, // prefix-only attack must fail
		{"../" + root + "/leak.md", root, false},
	}
	for _, c := range cases {
		got := canonicalPathIsSafe(c.path, c.allowedRoot)
		if got != c.want {
			t.Errorf("canonicalPathIsSafe(%q, %q) = %v, want %v", c.path, c.allowedRoot, got, c.want)
		}
	}
}

func TestDefaultBlobRoot_HonorsOverride(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", "/tmp/crew-custom")
	got, err := defaultBlobRoot()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "/tmp/crew-custom/memory/versions"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
