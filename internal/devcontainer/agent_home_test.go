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
