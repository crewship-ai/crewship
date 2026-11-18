package orchestrator

import (
	"context"
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
	cmd := exec.CommandContext(ctx, cfg.Cmd[0], cfg.Cmd[1:]...)
	cmd.Env = append(append([]string(nil), c.baseEnv...), cfg.Env...)
	cmd.Stdin = cfg.Stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("private tmux fixture command failed: %w", err)
	}
	return &provider.ExecResult{ExecID: "completed-fixture", Reader: io.NopCloser(strings.NewReader(string(out)))}, nil
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
	seed := exec.CommandContext(ctx, "tmux", "-f", "/dev/null", "new-session", "-d", "-s", "fixture-seed", "sleep 60")
	seed.Env = append(append([]string(nil), baseEnv...), "NATIVE_REVOKED_TOKEN=synthetic-old-token")
	if err := seed.Run(); err != nil {
		t.Fatal("start private fixture tmux server", err)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-server").Run() }) // TMUX_TMPDIR still selects only this fixture.
	file := filepath.Join(root, "native-secret")
	if err := os.WriteFile(file, []byte("synthetic-file"), 0400); err != nil {
		t.Fatal(err)
	}
	container := &nativeTmuxHostFixture{baseEnv: baseEnv}
	container.inspectResult.running = false
	container.inspectResult.exitCode = 0
	o := New(container, newMemState(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	runID := fmt.Sprintf("native-tmux-%d-%d", os.Getpid(), time.Now().UnixNano())
	for _, path := range derivedPaths("fixture", runID) {
		if strings.HasPrefix(path, "/tmp/") {
			defer os.Remove(path)
		}
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMUX_TMPDIR=" + root, "NATIVE_CURRENT_TOKEN=synthetic-current-token", "NATIVE_CURRENT_PATH=" + file}
	command := `test -z "${NATIVE_REVOKED_TOKEN+x}" && test "$NATIVE_CURRENT_TOKEN" = synthetic-current-token && test -f "$NATIVE_CURRENT_PATH" && sh -c 'test "$NATIVE_CURRENT_TOKEN" = synthetic-current-token' && printf NATIVE_TMUX_OK`
	wrapper, err := o.setupTmuxExec(ctx, "private-host-fixture", []string{"sh", "-c", command}, "fixture", runID, env)
	if err != nil {
		t.Fatal(err)
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
	for _, kind := range []string{"env file", "args file", "inner script", "FIFO", "exit file"} {
		if _, err := os.Stat(derivedPaths("fixture", runID)[kind]); !os.IsNotExist(err) {
			t.Errorf("tmux %s survived completed run", kind)
		}
	}
}
