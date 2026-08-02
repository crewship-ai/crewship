package devcontainer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnsureAgentHomeOwnership(t *testing.T) {
	var gotCmd, gotUser string
	exec := func(_ context.Context, containerID string, cmd []string, user string, _ []string) (string, int, error) {
		if containerID != "test-container" {
			t.Errorf("containerID = %q", containerID)
		}
		gotCmd, gotUser = strings.Join(cmd, " "), user
		return "", 0, nil
	}

	if err := EnsureAgentHomeOwnership(context.Background(), "test-container", exec); err != nil {
		t.Fatalf("EnsureAgentHomeOwnership: %v", err)
	}

	// Must run as root — the whole point is that 1001 cannot fix this itself.
	if gotUser != "0:0" {
		t.Errorf("user = %q, want 0:0", gotUser)
	}
	for _, want := range []string{"mkdir -p /home/agent", "chown -R 1001:1001 /home/agent", "chmod 755 /home/agent"} {
		if !strings.Contains(gotCmd, want) {
			t.Errorf("command %q missing %q", gotCmd, want)
		}
	}
}

func TestEnsureAgentHomeOwnership_Failures(t *testing.T) {
	transport := errors.New("transport down")
	execErr := func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		return "", 0, transport
	}
	if err := EnsureAgentHomeOwnership(context.Background(), "c", execErr); err == nil {
		t.Error("expected error when exec fails")
	}

	execExit := func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		return "chown: cannot access", 1, nil
	}
	err := EnsureAgentHomeOwnership(context.Background(), "c", execExit)
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "cannot access") {
		t.Errorf("error %v should carry the container output", err)
	}
}

// /opt/crew-tools is the crew's persistent tool directory: a named volume, on
// the agent's PATH via scripts/entrypoint.sh, and the place `crewship` installs
// anything meant to survive a container recreation.
//
// Nothing created it. `grep -rn crew-tools` over the tree finds the mount, the
// backup collector and the PATH entry — and no mkdir and no chown anywhere in
// the image build. A Docker named volume initialises from the image content at
// its mount point, so with the path absent from the image the volume comes up
// root-owned 0755 while the container runs as uid 1001. The only thing that
// then tries to populate it is the entrypoint:
//
//	mkdir -p /opt/crew-tools/bin 2>/dev/null || true
//
// which runs as 1001, fails, and is swallowed twice over — stderr to /dev/null
// and a `|| true` guard that exists so a failure here cannot kill PID 1. So the
// directory on the agent's PATH does not exist, nothing can be installed into
// it, and no error is produced anywhere. Caught by the runtime-conformance
// harness on docker 28.0.4, which writes as the agent and reports what happened
// (#1672).
//
// /home/agent had exactly this problem and was fixed by chowning it during the
// build; this extends that fix to its twin rather than inventing a second
// mechanism.
func TestEnsureAgentHomeOwnership_CoversTheToolsVolume(t *testing.T) {
	var got string
	exec := func(_ context.Context, _ string, cmd []string, user string, _ []string) (string, int, error) {
		if user != "0:0" {
			t.Errorf("ran as %q, want root — the whole point is fixing ownership the agent cannot fix itself", user)
		}
		got = strings.Join(cmd, " ")
		return "", 0, nil
	}
	if err := EnsureAgentHomeOwnership(context.Background(), "c", exec); err != nil {
		t.Fatalf("EnsureAgentHomeOwnership: %v", err)
	}

	// Both paths, each created AND handed to the agent. Asserting the path is
	// merely mentioned would pass on a command that mkdirs it and leaves it
	// root-owned, which is the exact bug.
	for _, path := range []string{"/home/agent", "/opt/crew-tools"} {
		if !strings.Contains(got, "mkdir -p "+path) {
			t.Errorf("command does not create %s: %s", path, got)
		}
		if !strings.Contains(got, "chown -R 1001:1001 "+path) {
			t.Errorf("command does not hand %s to the agent: %s", path, got)
		}
	}
}
