//go:build linux

package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type stopProcessContainer struct {
	provider.ContainerProvider
	dockerID string
}

func (c stopProcessContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	args := cfg.Cmd
	if c.dockerID != "" {
		args = append([]string{"docker", "exec", "--user", "1001", c.dockerID}, args...)
	}
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return nil, err
	}
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(string(out)))}, nil
}
func TestStopAgent_RealDirectProcess(t *testing.T) {
	dockerID := os.Getenv("CREWSHIP_STOP_TEST_CONTAINER")
	c := stopProcessContainer{dockerID: dockerID}
	o := New(c, nil, slog.Default())
	req := AgentRunRequest{AgentID: "stop-test", AgentSlug: "stop-test", RunID: NewRunID(), ContainerID: "test"}
	ctx, finish := o.trackAgentRun(context.Background(), &req)
	if err := req.ExecGate(ctx); err != nil {
		t.Fatal(err)
	}
	args := directRunCommand(req.RunID, []string{"sleep", "60"})
	if dockerID != "" {
		args = append([]string{"docker", "exec", "--user", "1001", dockerID}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		finish()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); finish(); done <- err }()
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = c.Exec(clean, provider.ExecConfig{Cmd: []string{"sh", "-c", directRunProbe(req.RunID, true)}})
		_ = cmd.Process.Kill()
		_, _ = c.Exec(clean, provider.ExecConfig{Cmd: []string{"rm", "-f", directRunPIDFile(req.RunID)}})
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive, err := o.RunIsAliveAt(context.Background(), RunLocation{ContainerID: "test", AgentSlug: req.AgentSlug, RunID: req.RunID})
		if err == nil && alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no live process: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := o.StopAgent(stopCtx, req.AgentID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop returned before process exit")
	}
}
