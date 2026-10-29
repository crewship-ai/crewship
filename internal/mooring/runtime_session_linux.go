//go:build linux

package mooring

import (
	"context"
	"os/exec"
)

func startSessionRuntimeCommand(ctx context.Context, c SessionRuntimeConfig, cmd *exec.Cmd) (*SessionRuntime, error) {
	process, err := startRuntimeSelected(ctx, c.Runtime, cmd, true)
	if err != nil {
		return nil, err
	}
	s, err := newSessionRuntime(process, c)
	if err != nil {
		_ = process.Close()
		return nil, err
	}
	return s, nil
}
