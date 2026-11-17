//go:build !windows

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
	for _, shell := range []string{"sh", "bash"} {
		t.Run(shell, func(t *testing.T) {
			binary, err := exec.LookPath(shell)
			if err != nil {
				t.Fatalf("required POSIX shell: %v", err)
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

func TestMiseReadinessIgnoresProjectWorkingDirectory(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "mise.toml"), []byte("invalid untrusted project configuration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	fake := `#!/bin/sh
if test -f mise.toml; then echo 'project config was loaded'; exit 17; fi
test "$*" = 'ls --global --missing --no-header' || exit 18
test "$MISE_CEILING_PATHS" = / || exit 19
`
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(fake), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", miseMissingInstallCheck)
	cmd.Dir = project
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("project WORKDIR leaked into global readiness: %v %s", err, out)
	}
}

func TestNativeToolPathOrderMatchesProfileForLegacyAndNew(t *testing.T) {
	for _, base := range []string{
		"/opt/mise/data/shims:/usr/bin:/opt/crew-tools/bin:/opt/feature/bin:/bin",
		strings.Join(AgentToolPathDirs, ":") + ":/usr/bin:/opt/feature/bin:/bin",
	} {
		for _, shell := range []string{"/bin/sh", "/bin/bash"} {
			cmd := exec.Command(shell, "-c", AgentToolProfile()+`printf '%s' "$PATH"`)
			cmd.Env = []string{"PATH=" + base}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v %s", shell, err, out)
			}
			if string(out) != NativeToolPath(base) {
				t.Fatalf("profile/runtime order differs: %q != %q", out, NativeToolPath(base))
			}
		}
	}
}
