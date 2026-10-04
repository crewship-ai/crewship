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
// externalExecPreparation is supplied only by a provider implementing the
// current boot's static admission gate; the backup adapter does not emulate it.
type externalExecPreparation interface {
	PrepareExternalExec(context.Context, string, []string, []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error)
}

func (r *Router) backupDockerOps(cli *client.Client) *backup.MobyDockerOps {
	return &backup.MobyDockerOps{
		Client: cli,
		Prepare: func(ctx context.Context, id string, cmd, env []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error) {
			active := r.activeContainer()
			if p, ok := active.(externalExecPreparation); ok {
				return p.PrepareExternalExec(ctx, id, cmd, env)
			}
			if g, ok := active.(externalExecGuard); ok {
				before, after, err := g.GuardExternalExec(ctx, id)
				return cmd, env, before, after, err
			}
			return cmd, env, nil, nil, nil
		},
	}
}
