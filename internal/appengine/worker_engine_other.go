//go:build !linux

package appengine

import (
	"context"
	"errors"
	"fmt"
)

var errEngineSandboxPlatform = errors.New("engine sandbox requires Linux")

// PrepareEngineSandbox fails closed: the engine is only confined on Linux.
func PrepareEngineSandbox() error { return errEngineSandboxPlatform }

// ExecEngineSandbox fails closed: the engine is only confined on Linux.
func ExecEngineSandbox([]string) error { return errEngineSandboxPlatform }

func (e WorkerEngine) execEngine(context.Context, string, []string) error {
	return fmt.Errorf("%w: %v", ErrEngineUnavailable, errEngineSandboxPlatform)
}

func readBounded(string) ([]byte, error) {
	return nil, fmt.Errorf("%w: %v", ErrEngineUnavailable, errEngineSandboxPlatform)
}
