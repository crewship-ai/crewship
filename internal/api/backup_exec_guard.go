package api

import (
	"context"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/backup"
)

// externalExecGuard is implemented by a container provider that guards
// execs it does not create itself (the Docker provider's egress fence).
type externalExecGuard interface {
	GuardExternalExec(ctx context.Context, containerID string) (beforeStart, afterStart func(context.Context) error, err error)
}

// backupDockerOps builds the backup package's Docker adapter with the
// provider's exec guard, so backup flush hooks, restore scripts and memory
// imports cannot run in a fenced crew's container start whose fence is not
// confirmed (#1368). The provider is resolved per exec, so wiring order
// does not matter; without one there is nothing to guard.
func (r *Router) backupDockerOps(cli *client.Client) *backup.MobyDockerOps {
	return &backup.MobyDockerOps{
		Client: cli,
		Wrap: func(ctx context.Context, id string, cmd, env []string) ([]string, []string, error) {
			if g, ok := r.activeContainer().(interface {
				WrapExternalExec(context.Context, string, []string, []string) ([]string, []string, error)
			}); ok {
				return g.WrapExternalExec(ctx, id, cmd, env)
			}
			return cmd, env, nil
		},
		Guard: func(ctx context.Context, containerID string) (func(context.Context) error, func(context.Context) error, error) {
			if g, ok := r.activeContainer().(externalExecGuard); ok {
				return g.GuardExternalExec(ctx, containerID)
			}
			return nil, nil, nil
		},
	}
}
