//go:build integration

package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Execute the production tmux setup against a private host fixture. No Docker
// daemon, real login or existing tmux socket is involved.
type nativeTmuxHostFixture struct {
	mockContainer
	commands [][]string
	baseEnv  []string
}

func (c *nativeTmuxHostFixture) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.commands = append(c.commands, append([]string(nil), cfg.Cmd...))
	argv := append([]string{"-c", `umask 022; exec "$@"`, "fixture"}, cfg.Cmd...)
	cmd := exec.CommandContext(ctx, "/bin/sh", argv...)
	cmd.Env = append(append([]string(nil), c.baseEnv...), cfg.Env...)
	cmd.Stdin = cfg.Stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("private tmux fixture command failed: %w", err)
	}
	return &provider.ExecResult{ExecID: "completed-fixture", Reader: io.NopCloser(strings.NewReader(string(out)))}, nil
}

func nativeTmuxCLICommand(t *testing.T, ctx context.Context, argv, env []string, home string, failNewSession bool) *exec.Cmd {
	t.Helper()
	socketRoot := filepath.Join(home, "tmux-sockets")
	if err := os.Mkdir(socketRoot, 0700); err != nil {
		t.Fatal(err)
	}
	baseEnv := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMUX_TMPDIR=" + socketRoot, "TMUX="}
	seed := exec.CommandContext(ctx, "tmux", "-f", "/dev/null", "new-session", "-d", "-s", "fixture-seed", "sleep 90")
	seed.Env = append(append([]string(nil), baseEnv...), "NATIVE_REVOKED_TOKEN=synthetic-old-token")
	if err := seed.Run(); err != nil {
		t.Fatal("start private native CLI tmux server", err)
	}
	privateDir := ""
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stop := exec.CommandContext(cleanup, "tmux", "kill-server")
		stop.Env = baseEnv
		if err := stop.Run(); err != nil {
			t.Errorf("private native CLI tmux cleanup failed: %v", err)
		}
		if privateDir != "" {
			os.RemoveAll(privateDir)
		}
	})
	env = overrideEnv(env, "TMUX_TMPDIR", socketRoot)
	container := &nativeTmuxHostFixture{baseEnv: baseEnv}
	container.inspectResult.exitCode = 0
	o := New(container, newMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID := fmt.Sprintf("native-cli-tmux-%d-%d", os.Getpid(), time.Now().UnixNano())
	privateDir = tmuxExecDir("fixture", runID)
	wrapper, err := o.setupTmuxExec(ctx, "private-cli-fixture", argv, "fixture", runID, env)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, wrapper[0], wrapper[1:]...)
	cmd.Env = env
	cmd.Dir = home
	if failNewSession {
		cmd.Env = nativeTmuxMissingClientEnv(t, cmd.Env, home)
	}
	return cmd
}

// Simulate an actual unavailable tmux client without contacting any socket.
// The inner CLI still receives the full fresh-exec PATH captured at setup.
func nativeTmuxMissingClientEnv(t *testing.T, env []string, home string) []string {
	t.Helper()
	bin := filepath.Join(home, "client-without-tmux")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rm", "mkfifo", "cat"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	return overrideEnv(overrideEnv(env, "PATH", bin), "LC_ALL", "C")
}

func TestNativeTmuxMissingSnapshotCannotSucceed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	home := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMUX_TMPDIR=" + home, "TMUX="}
	c := &nativeTmuxHostFixture{baseEnv: env}
	c.inspectResult.exitCode = 0
	o := New(c, newMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID := fmt.Sprintf("native-tmux-missing-%d-%d", os.Getpid(), time.Now().UnixNano())
	privateDir := tmuxExecDir("fixture", runID)
	t.Cleanup(func() { os.RemoveAll(privateDir) })
	wrapper, err := o.setupTmuxExec(ctx, "private-fixture", []string{"sh", "-c", "printf UNEXPECTED_TOOL"}, "fixture", runID, env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(derivedPaths("fixture", runID)["env file"]); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, wrapper[0], wrapper[1:]...)
	cmd.Env = nativeTmuxMissingClientEnv(t, env, home)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("tmux fallback reported success without starting the tool")
	}
	if strings.Contains(string(out), "UNEXPECTED_TOOL") {
		t.Fatal("tool started despite a missing native environment snapshot")
	}
	if _, err := os.Stat(privateDir); !os.IsNotExist(err) {
		t.Fatal("failed bootstrap retained the private run directory")
	}
}

func TestNativeTmuxRefusesExistingSetupDirectory(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			home := t.TempDir()
			runID := fmt.Sprintf("native-tmux-collision-%d-%d", os.Getpid(), time.Now().UnixNano())
			privateDir := tmuxExecDir("fixture", runID)
			target := privateDir
			if symlink {
				target = t.TempDir()
				if err := os.Symlink(target, privateDir); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Remove(privateDir) })
			} else {
				if err := os.Mkdir(privateDir, 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(privateDir) })
			}
			sentinel := filepath.Join(target, "existing-sentinel")
			if err := os.WriteFile(sentinel, []byte("preserve-fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
			c := &nativeTmuxHostFixture{baseEnv: env}
			c.inspectResult.exitCode = 0
			o := New(c, newMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
			if _, err := o.setupTmuxExec(ctx, "private-fixture", []string{"true"}, "fixture", runID, env); err == nil {
				t.Fatal("existing directory or symlink was reused")
			}
			got, err := os.ReadFile(sentinel)
			if err != nil || string(got) != "preserve-fixture" {
				t.Fatal("refused setup changed an existing directory")
			}
			if _, err := os.Stat(filepath.Join(target, "env")); !os.IsNotExist(err) {
				t.Fatal("credential snapshot was written into an existing directory")
			}
		})
	}
}

func TestNativeTmuxCurrentRunCannotInheritRevokedToken(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatal("native tmux credential qualification requires tmux")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	root := t.TempDir()
	t.Setenv("TMUX_TMPDIR", root)
	t.Setenv("TMUX", "")
	t.Setenv("HOME", root)
	baseEnv := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMUX_TMPDIR=" + root, "TMUX="}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "native-tmux-tool"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	baseEnv[0] = "PATH=" + bin + ":" + os.Getenv("PATH")
	seed := exec.CommandContext(ctx, "tmux", "-f", "/dev/null", "new-session", "-d", "-s", "fixture-seed", "sleep 60")
	seed.Env = append(append([]string(nil), baseEnv...), "NATIVE_REVOKED_TOKEN=synthetic-old-token", "NATIVE_REVOKED_PATH=/synthetic/old/path")
	if err := seed.Run(); err != nil {
		t.Fatal("start private fixture tmux server", err)
	}
	privateDir := ""
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stop := exec.CommandContext(cleanup, "tmux", "kill-server")
		stop.Env = baseEnv
		if err := stop.Run(); err != nil {
			t.Errorf("private tmux cleanup failed: %v", err)
		}
		if privateDir != "" {
			os.RemoveAll(privateDir)
		}
	})
	unexpectedShell := filepath.Join(root, "unexpected-shell")
	shellMarker := filepath.Join(root, "unexpected-shell-started")
	if err := os.WriteFile(unexpectedShell, []byte("#!/bin/sh\nprintf x > '"+shellMarker+"'\nexit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setShell := exec.CommandContext(ctx, "tmux", "set-option", "-g", "default-shell", unexpectedShell)
	setShell.Env = baseEnv
	if err := setShell.Run(); err != nil {
		t.Fatal("configure isolated default-shell fixture", err)
	}
	file := filepath.Join(root, "native-secret")
	if err := os.WriteFile(file, []byte("synthetic-file"), 0400); err != nil {
		t.Fatal(err)
	}
	container := &nativeTmuxHostFixture{baseEnv: baseEnv}
	container.inspectResult.running = false
	container.inspectResult.exitCode = 0
	o := New(container, newMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID := fmt.Sprintf("native-tmux-%d-%d", os.Getpid(), time.Now().UnixNano())
	privateDir = tmuxExecDir("fixture", runID)
	const opaque = "fixture 'quoted'\n$HOME stays literal\n"
	opaqueHash := fmt.Sprintf("%x", sha256.Sum256([]byte(opaque)))
	// PATH deliberately comes only from the image/fresh-exec fixture.
	env := []string{"HOME=" + root, "TMUX_TMPDIR=" + root, "NATIVE_CURRENT_TOKEN=synthetic-current-token", "NATIVE_CURRENT_PATH=" + file, "NATIVE_CURRENT_OPAQUE=" + opaque}
	command := `test -z "${NATIVE_REVOKED_TOKEN+x}" && test -z "${NATIVE_REVOKED_PATH+x}" && test "$NATIVE_CURRENT_TOKEN" = synthetic-current-token && test "$(cat "$NATIVE_CURRENT_PATH")" = synthetic-file && native-tmux-tool && sh -c 'test "$NATIVE_CURRENT_TOKEN" = synthetic-current-token' && test "$(printf %s "$NATIVE_CURRENT_OPAQUE" | sha256sum | cut -d' ' -f1)" = ` + opaqueHash + ` && printf NATIVE_TMUX_OK`
	wrapper, err := o.setupTmuxExec(ctx, "private-host-fixture", []string{"sh", "-c", command}, "fixture", runID, env)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(privateDir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("native tmux directory must be private mode0700")
	}
	for _, kind := range []string{"env file", "args file"} {
		info, err := os.Stat(derivedPaths("fixture", runID)[kind])
		if err != nil || info.Mode().Perm() != 0600 {
			t.Errorf("native tmux %s must be private mode0600", kind)
		}
	}
	for _, argv := range container.commands {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "synthetic-current-token") {
			t.Error("credential setup material appeared in process arguments")
		}
		for _, word := range strings.Fields(joined) {
			decoded, err := base64.StdEncoding.DecodeString(strings.Trim(word, "'"))
			if err == nil && strings.Contains(string(decoded), "synthetic-current-token") {
				t.Error("encoded credential setup material appeared in process arguments")
			}
		}
	}
	run := exec.CommandContext(ctx, wrapper[0], wrapper[1:]...)
	run.Env = baseEnv
	out, err := run.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "NATIVE_TMUX_OK") {
		t.Fatalf("actual tmux shell did not receive only its current run: exit%v", err)
	}
	if _, err := os.Stat(shellMarker); !os.IsNotExist(err) {
		t.Fatal("tmux invoked its inherited default-shell instead of direct argv")
	}
	for _, kind := range []string{"env file", "args file", "inner script", "FIFO", "exit file"} {
		if _, err := os.Stat(derivedPaths("fixture", runID)[kind]); !os.IsNotExist(err) {
			t.Errorf("tmux %s survived completed run", kind)
		}
	}
}
