//go:build linux && runoutputrepro

package orchestrator

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type localOutputFixture struct {
	provider.ContainerProvider
	env []string
}

func (c localOutputFixture) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	cmd := exec.CommandContext(ctx, cfg.Cmd[0], cfg.Cmd[1:]...)
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}
	return &provider.ExecResult{ExecID: "fixture", Reader: io.NopCloser(strings.NewReader(string(out)))}, nil
}

// This expected-red reproducer records the default transport defect in #2845.
// It is explicitly opt-in, not passing durable-transport acceptance. Use a
// private tmux socket and synthetic output; no Docker or live controller.
func TestTmuxRunContinuesAfterOutputReaderLoss(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal("runoutputrepro requires tmux on PATH")
	}
	dir := t.TempDir()
	socket := filepath.Join(dir, "tmux.sock")
	shim := filepath.Join(dir, "tmux")
	if err = os.WriteFile(shim, []byte("#!/bin/sh\nexec '"+tmux+"' -S '"+socket+"' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "TMUX=", "PATH="+dir+":"+os.Getenv("PATH"))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, tmux, "-S", socket, "kill-server").Run()
	})
	runID := NewRunID()
	slug := "reader-loss"
	stem := "/tmp/" + TmuxSessionName(slug, runID)
	t.Cleanup(func() {
		for _, ext := range []string{".args", ".sh", ".fifo", ".exit", ".env", ".output"} {
			_ = os.Remove(stem + ext)
		}
	})
	release, done := filepath.Join(dir, "release"), filepath.Join(dir, "done")
	payload := "printf 'before\\n'; while [ ! -f '" + release + "' ]; do sleep 0.02; done; printf 'after\\n'; touch '" + done + "'"
	o := New(localOutputFixture{env: env}, newMemState(), slog.Default())
	o.tmuxCacheStore("local-fixture", true)
	wrapper, err := o.setupTmuxExec(t.Context(), "local-fixture", []string{"sh", "-c", payload}, slug, runID, []string{"PATH=" + dir + ":" + os.Getenv("PATH")})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cmd := exec.Command(wrapper[0], wrapper[1:]...)
	cmd.Env = env
	cmd.Stdout = writer
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
	})
	_ = reader.SetReadDeadline(time.Now().Add(5 * time.Second))
	first, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil || first != "before\n" {
		t.Fatalf("agent did not start: %q %v", first, err)
	}
	// Kill only the outer observer process group. The private tmux server and
	// its agent session are detached, so they are not direct signal targets.
	if err = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	reaped = true
	reader.Close()
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(done); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	terminal, _ := os.ReadFile(stem + ".exit")
	t.Fatalf("agent did not finish after reader loss; retained exit=%q", terminal)
}
