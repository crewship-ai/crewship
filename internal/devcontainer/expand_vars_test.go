package devcontainer

import (
	"strings"
	"testing"
)

func TestExpandVars_DevcontainerID(t *testing.T) {
	out := ExpandVars("dind-var-lib-docker-${devcontainerId}", "cmoiqveyn000bc572009a")
	if strings.Contains(out, "$") {
		t.Fatalf("expected ${devcontainerId} to be expanded, got %q", out)
	}
	if !strings.HasPrefix(out, "dind-var-lib-docker-") {
		t.Fatalf("prefix lost: %q", out)
	}
	// Suffix must be a Docker-volume-safe ID (hex chars only, length 16).
	suffix := strings.TrimPrefix(out, "dind-var-lib-docker-")
	if len(suffix) != 16 {
		t.Fatalf("expected 16-char hex suffix, got %d chars: %q", len(suffix), suffix)
	}
	for _, r := range suffix {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			t.Fatalf("suffix contains non-hex char %q in %q", r, suffix)
		}
	}
}

func TestExpandVars_StableForSameCrew(t *testing.T) {
	a := ExpandVars("vol-${devcontainerId}", "crew-1")
	b := ExpandVars("vol-${devcontainerId}", "crew-1")
	if a != b {
		t.Fatalf("expected stable ID for same crew: %q vs %q", a, b)
	}
}

func TestExpandVars_DistinctForDifferentCrews(t *testing.T) {
	a := ExpandVars("vol-${devcontainerId}", "crew-1")
	b := ExpandVars("vol-${devcontainerId}", "crew-2")
	if a == b {
		t.Fatalf("expected distinct IDs for different crews, both got %q", a)
	}
}

func TestExpandVars_NoCrewID(t *testing.T) {
	out := ExpandVars("vol-${devcontainerId}", "")
	if !strings.Contains(out, "${devcontainerId}") {
		t.Fatalf("expected variable preserved when crewID is empty, got %q", out)
	}
}

func TestExpandVars_NoVarsToReplace(t *testing.T) {
	in := "/var/run/docker.sock"
	if got := ExpandVars(in, "anything"); got != in {
		t.Fatalf("expected unchanged %q, got %q", in, got)
	}
}

func TestExpandVars_Empty(t *testing.T) {
	if got := ExpandVars("", "crew"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
