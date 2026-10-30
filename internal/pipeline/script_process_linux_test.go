//go:build linux

package pipeline

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScriptProcessStop_KillsChildBeforeSideEffect(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Fatal("Linux script process tests require setsid from util-linux")
	}
	root := t.TempDir()
	control := filepath.Join(root, "control")
	marker := filepath.Join(root, "unwanted-output")
	cmd := exec.Command("setsid", "sh", "-c", scriptProcessWrapper, "crewship-script", control, strings.Repeat("a", 32), "sh", "-c", `trap '' TERM; (sleep 1; touch "$1") & wait`, "test", marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Redirection creates pid before printf writes it. Keep the nonempty read;
	// observing the file alone can pass an empty expected PID to the stop guard.
	var pid []byte
	var err error
	for i := 0; i < 100; i++ {
		pid, err = os.ReadFile(filepath.Join(control, "pid"))
		if err == nil && len(pid) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || len(pid) == 0 {
		t.Fatalf("PID publication not ready: pid=%q err=%v", pid, err)
	}
	// Capture guard diagnostics for any future failure without weakening it.
	stop := exec.Command("sh", "-x", "-c", scriptStopWrapper, "crewship-script-stop", control, string(pid))
	if out, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("stop failed for expected PID %q: %v %s", pid, err, out)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child survived cancellation: %v", err)
	}
}

// Docker exec may start setsid as a process-group leader, making setsid fork.
// Without --wait it then reports 0 even when the actual script fails.
func TestScriptProcessWrapper_PreservesForkedChildExit(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Fatal("Linux script process tests require setsid from util-linux")
	}
	root := t.TempDir()
	artifact := filepath.Join(root, "artifact.txt")
	command := exec.Command("sh", "-c", `umask 022; exec "$@"`, "fixture", "setsid", "--fork", "--wait", "sh", "-c", scriptProcessWrapper, "fixture", filepath.Join(root, "control"), strings.Repeat("a", 32), "sh", "-c", `echo result > "$1"; echo partial; exit 7`, "fixture", artifact)
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(string(output), "partial") {
		t.Fatalf("script failure was lost: %q %v", output, err)
	}
	info, statErr := os.Stat(artifact)
	if statErr != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("control umask leaked into artifact: %v %v", info, statErr)
	}

}

// These synchronous wrapper fixtures have no user-created background children.
// Bound both command lifetime and an inherited pipe; the orphan topology below
// remains an explicit acceptance gap, not a kill-only join assertion.
func scriptGuardFixtureCommand(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd
}

// These run the actual shell guards, independently of the provider state fake.
// No optional utility availability may become a prerequisite for user code.
func TestScriptWrapper_OptionalToolsAbsentAndCollision(t *testing.T) {
	mkdir, err := exec.LookPath("mkdir")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mkdir, filepath.Join(bin, "mkdir")); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(root, "control")
	nonce := strings.Repeat("b", 32)
	run := func() ([]byte, error) {
		cmd := scriptGuardFixtureCommand(t, "/bin/sh", "-c", scriptProcessWrapper, "fixture", control, nonce, "/bin/sh", "-c", "printf payload")
		cmd.Env = []string{"PATH=" + bin}
		return cmd.CombinedOutput()
	}
	out, err := run()
	if err != nil {
		t.Fatalf("missing optional tools blocked user code: %q %v", out, err)
	}
	lines := strings.SplitN(string(out), "\n", 3)
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "crewship-script-created-v1:"+nonce+":") || lines[1] != "crewship-script-cleanup-v1:"+nonce+":-" || lines[2] != "payload" {
		t.Fatalf("receipt order/user output: %q", out)
	}
	if err := os.WriteFile(filepath.Join(control, "sentinel"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 125 || strings.Contains(string(out), "crewship-script-created-v1") || strings.Contains(string(out), "payload") {
		t.Fatalf("collision authorized user code/ownership: %q %v", out, err)
	}
	if got, err := os.ReadFile(filepath.Join(control, "sentinel")); err != nil || string(got) != "keep" {
		t.Fatalf("collision modified existing content: %q %v", got, err)
	}
}

func TestScriptCleanupGuard_RealShellFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, ps string
		remove   bool
	}{
		{"empty group", "printf '999 S\\n'", true},
		{"live child", "printf '4321 S\\n'", false},
		{"ps failure", "exit 1", false},
		{"empty enumeration", "exit 0", false},
		{"malformed enumeration", "printf 'invalid\\n'", false},
		{"malformed zombie status", "printf '4321 Zgarbage\\n'", false},
		{"malformed unrelated status", "printf '999 Zgarbage\\n'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			for _, tool := range []string{"stat", "awk", "rm"} {
				actual, err := exec.LookPath(tool)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(actual, filepath.Join(bin, tool)); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(bin, "ps"), []byte("#!/bin/sh\n"+tc.ps+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			control := filepath.Join(root, "control $literal; no-glob*")
			if err := os.Mkdir(control, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(control, "keep"), []byte("owned"), 0600); err != nil {
				t.Fatal(err)
			}
			identity, err := scriptGuardFixtureCommand(t, "stat", "-c", "%d:%i", "--", control).Output()
			if err != nil {
				t.Fatal(err)
			}
			fields := strings.Split(strings.TrimSpace(string(identity)), ":")
			cmd := scriptGuardFixtureCommand(t, "/bin/sh", "-c", scriptCleanupWrapper, "fixture", control, "4321", fields[0], fields[1])
			cmd.Env = []string{"PATH=" + bin}
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.remove {
				t.Fatalf("guard result: %q %v", out, err)
			}
			_, statErr := os.Stat(control)
			if tc.remove != os.IsNotExist(statErr) {
				t.Fatalf("wrong deletion outcome: %v", statErr)
			}
		})
	}
}

func TestScriptCleanupGuard_RejectsSymlinkAndChangedIdentity(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "owned")
	sibling := filepath.Join(root, "sibling")
	for _, dir := range []string{owned, sibling} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("sentinel"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := scriptGuardFixtureCommand(t, "stat", "-c", "%d:%i", "--", owned).Output()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSpace(string(identity)), ":")
	moved := owned + "-moved"
	if err := os.Rename(owned, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sibling, owned); err != nil {
		t.Fatal(err)
	}
	guard := func() {
		t.Helper()
		if out, err := scriptGuardFixtureCommand(t, "/bin/sh", "-c", scriptCleanupWrapper, "fixture", owned, "4321", fields[0], fields[1]).CombinedOutput(); err == nil {
			t.Fatalf("replacement accepted: %q", out)
		}
	}
	guard()
	if err := os.Remove(owned); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(owned, 0700); err != nil {
		t.Fatal(err)
	}
	guard()
	for _, dir := range []string{moved, sibling} {
		if got, err := os.ReadFile(filepath.Join(dir, "keep")); err != nil || string(got) != "sentinel" {
			t.Fatalf("unrelated identity modified: %q %v", got, err)
		}
	}
}

// Acceptance gap: no new real orphan/leader-exit fixture is claimed here.
// The provider state machine covers live-child retention. A real leader-exit
// fixture requires an isolated supervisor/subreaper with every child joined;
// the unsafe unbounded orphan fixture from r1 has been removed before execution.
