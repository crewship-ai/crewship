package devcontainer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The recorder turns the check into a RUN layer that names the binaries and
// runs as the agent user with the tool dirs on PATH.
func TestVerifyBinariesResolve_RecordedAsBuildLayer(t *testing.T) {
	rec := &dockerfileRecorder{}
	if err := VerifyBinariesResolve(context.Background(), "", []string{"claude", "codex"}, rec.exec); err != nil {
		t.Fatal(err)
	}
	if len(rec.groups) != 1 || rec.groups[0].user != "1001:1001" {
		t.Fatalf("expected one agent-user group, got %+v", rec.groups)
	}
	line := strings.Join(rec.groups[0].cmds, "\n")
	for _, want := range []string{"command -v", "claude", "codex", "/opt/mise/data/shims", "adapter CLI not installed"} {
		if !strings.Contains(line, want) {
			t.Errorf("recorded step lacks %q:\n%s", want, line)
		}
	}
	// Nothing to verify records nothing.
	empty := &dockerfileRecorder{}
	if err := VerifyBinariesResolve(context.Background(), "", nil, empty.exec); err != nil || len(empty.groups) != 0 {
		t.Errorf("empty list must be a no-op: err=%v groups=%+v", err, empty.groups)
	}
}

// A real exec that exits non-zero fails provisioning with the binary named;
// an exec transport error is reported as such.
func TestVerifyBinariesResolve_ExecOutcomes(t *testing.T) {
	failing := func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		return "crewship: adapter CLI not installed in this image: claude (PATH=/usr/bin)", 1, nil
	}
	err := VerifyBinariesResolve(context.Background(), "c1", []string{"claude"}, failing)
	if err == nil || !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("expected a failure naming claude, got %v", err)
	}
	broken := func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		return "", 0, errors.New("docker exec: connection refused")
	}
	err = VerifyBinariesResolve(context.Background(), "c1", []string{"claude"}, broken)
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected the transport error, got %v", err)
	}
	ok := func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		return "", 0, nil
	}
	if err := VerifyBinariesResolve(context.Background(), "c1", []string{"claude"}, ok); err != nil {
		t.Errorf("expected success, got %v", err)
	}
}
