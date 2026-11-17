package devcontainer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentToolProfileAfterLoginPathReset(t *testing.T) {
	// A system login profile may replace the image PATH. Exercise the actual
	// generated POSIX hook, including repeated sourcing and inherited children.
	for _, shell := range []string{"sh", "bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			binary, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("optional shell: %v", err)
			}
			script := "PATH=/usr/bin:/bin\n" + AgentToolProfile() + AgentToolProfile() + "exec sh -c 'printf %s \"$PATH\"'"
			out, err := exec.Command(binary, "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("shell/child: %v: %s", err, out)
			}
			for _, dir := range AgentToolPathDirs {
				if strings.Count(":"+string(out)+":", ":"+dir+":") != 1 {
					t.Fatalf("%s missing or duplicated: %s", dir, out)
				}
			}
		})
	}
}

func TestMisePreflightRejectsMissingInstallation(t *testing.T) {
	// A successful install/reshim command must not publish a ready image if
	// mise still reports a missing declared version. No tools/network/secrets.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mise"), []byte("#!/bin/sh\nprintf 'glab 999.0.0 (missing)\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	run := func(_ctx context.Context, _id string, cmd []string, _user string, _env []string) (string, int, error) {
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
		out, err := c.CombinedOutput()
		if e, ok := err.(*exec.ExitError); ok {
			return string(out), e.ExitCode(), nil
		}
		return string(out), 0, err
	}
	err := verifyMiseShims(context.Background(), "fixture", run)
	if !errors.Is(err, ErrMiseInstallFailed) || !strings.Contains(err.Error(), "missing mise installations: glab 999.0.0") {
		t.Fatalf("expected actionable failed preflight, got %v", err)
	}
}
