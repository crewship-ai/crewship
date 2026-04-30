package main

import (
	"strings"
	"testing"
)

func TestBuildExplainPrompt(t *testing.T) {
	got := buildExplainPrompt("r_abc", "10:00  [info/exec.start]  agent started\n10:01  [error/exec.error]  bad thing\n")
	if !strings.Contains(got, "r_abc") {
		t.Errorf("missing run id: %q", got)
	}
	if !strings.Contains(got, "Be concise") {
		t.Errorf("missing template lead-in: %q", got)
	}
	if !strings.Contains(got, "exec.error") {
		t.Errorf("missing entries content: %q", got)
	}
	// Trailing whitespace/newline should be trimmed by TrimSpace.
	if strings.HasSuffix(got, "\n\n") || strings.HasSuffix(got, " ") {
		t.Errorf("expected trailing whitespace stripped: %q", got[len(got)-10:])
	}
}

func TestBuildExplainPrompt_EmptyEntries(t *testing.T) {
	got := buildExplainPrompt("r_xyz", "")
	if !strings.Contains(got, "r_xyz") {
		t.Errorf("missing run id even with empty entries: %q", got)
	}
}
