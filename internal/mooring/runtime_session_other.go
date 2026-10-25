//go:build !linux

package mooring

import (
	"context"
	"os/exec"
)

func startSessionRuntimeCommand(context.Context, SessionRuntimeConfig, *exec.Cmd) (*SessionRuntime, error) {
	return nil, ErrRuntimeHandshake
}
