package crewstart

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

// NativeToolExecEnv repairs the PATH contract for an already-created runtime.
// Read only PATH, through a non-login exec as the agent. Never copy a run's
// environment or the container's other environment values into a new exec.
func NativeToolExecEnv(ctx context.Context, container provider.ContainerProvider, id string, env []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := container.Exec(ctx, provider.ExecConfig{ContainerID: id, User: "1001:1001", Cmd: []string{"/bin/sh", "-c", `printf '%s' "$PATH"`}})
	if err != nil {
		return nil, fmt.Errorf("read native tool PATH: %w", err)
	}
	if result == nil || result.Reader == nil {
		return nil, fmt.Errorf("read native tool PATH: missing exec output")
	}
	defer result.Reader.Close()
	stop := context.AfterFunc(ctx, func() { _ = result.Reader.Close() })
	defer stop()
	const limit = 32768
	path, err := io.ReadAll(io.LimitReader(result.Reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read native tool PATH: %w", err)
	}
	running, code, err := container.ExecInspect(ctx, result.ExecID)
	if err != nil {
		return nil, fmt.Errorf("inspect native tool PATH: %w", err)
	}
	if running || code != 0 || len(path) > limit || strings.ContainsAny(string(path), "\x00\r\n") {
		return nil, fmt.Errorf("read native tool PATH: invalid or incomplete exec result (running=%t, exit=%d)", running, code)
	}
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, "PATH=") {
			out = append(out, item)
		}
	}
	return append(out, "PATH="+devcontainer.NativeToolPath(string(path))), nil
}
