package main

import "testing"

func TestCanonicalPathIsSafe(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"AGENT.md", true},
		{"/var/lib/crewship/memory/topics/crew_a/learned-2026-05-17.md", true},
		{"../etc/passwd", false},
		{"a/../b", false}, // any ".." anywhere in the path is rejected
		{"safe/relative/path", true},
	}
	for _, c := range cases {
		got := canonicalPathIsSafe(c.path)
		if got != c.want {
			t.Errorf("canonicalPathIsSafe(%q) = %v, want %v", c.path, got, c.want)
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
