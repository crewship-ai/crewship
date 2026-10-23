//go:build !linux

package mooring

import (
	"context"
	"errors"
	"os/exec"
)

func startRuntime(context.Context, RuntimeConfig, *exec.Cmd) (*Runtime, error) {
	return nil, errors.New("mooring managed runtime requires Linux inherited sockets")
}
