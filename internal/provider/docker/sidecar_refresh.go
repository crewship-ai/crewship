package docker

// Self-healing of crew runtimes that still serve the crewship-sidecar binary
// of a previous server version (#3964).
//
// The sidecar is bind-mounted from a staged copy that an upgrade replaces
// atomically. A bind mount pins the inode it was created with, so a container
// created before the upgrade keeps executing the old binary for as long as it
// exists, and restarting it changes nothing. The orchestrator already detects
// that (sidecar.stale), but until #3964 it only reported it: agents silently
// ran without whatever the new sidecar adds until an operator ran
// `crewship crew restart-agents`.
//
// Each crew container is therefore stamped with the hash of the sidecar it
// mounts (crewSidecarHashLabel). Reconcile compares that stamp with the binary
// staged now, and a mismatch, or no stamp at all on a container from before
// this change, is handled like image drift, with the same asymmetry:
//
//   - stopped: remove it and create a fresh one. Nothing runs in it, and the
//     named home/tools volumes and the host-bound /workspace, /output and /crew
//     data survive, exactly as on the image-drift recreate.
//   - running, reached through AcquireCrewRuntimeUse: try to take the crew's
//     runtime gate exclusively WITHOUT waiting. If nobody holds the runtime and
//     the controller confirms it is idle, stop it; the next reconcile, still
//     before the caller's run starts, recreates it. Any doubt (another run
//     holds the gate, an exclusive change is queued, the idle verifier says no
//     or cannot tell) keeps today's behaviour: reuse the container and let the
//     sidecar.stale signal say so. The next run start retries, so the crew
//     converges at its first idle point.
//   - running, reached through a bare EnsureCrewRuntime call: reuse. A caller
//     that holds no reservation cannot authorise replacing a runtime.

import (
	"context"
	"errors"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/provider"
)

// crewSidecarHashLabel carries the hash (sha256, first 12 hex chars, the same
// shape the sidecar reports on /health) of the crewship-sidecar binary the
// container was created with.
const crewSidecarHashLabel = "crewship.sidecar-hash"

// mountedSidecarHash is the hash of the binary a container created now would
// mount: the staged copy at cfg.SidecarBinaryPath. Deliberately not
// ExpectedSidecarHash, which prefers the hash baked in at build time. When the
// deployed sidecar artifact itself is stale the two differ, recreating cannot
// help (a new container mounts the same old file), and comparing against the
// build hash would recreate an idle crew on every run. "" when the binary is
// unset or unreadable: no opinion, nothing is recreated.
func (p *Provider) mountedSidecarHash() string { return p.onDiskSidecarHash() }

func sidecarHashOf(cfg *container.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Labels[crewSidecarHashLabel]
}

// sidecarRefreshPendingError is reconcile asking AcquireCrewRuntimeUse to
// replace a running runtime that serves an outdated sidecar. It never leaves
// this package: AcquireCrewRuntimeUse either recycles the runtime or retries
// with the outdated one tolerated.
type sidecarRefreshPendingError struct {
	ContainerID string
	CurrentHash string
	DesiredHash string
}

func (e *sidecarRefreshPendingError) Error() string {
	return "crew runtime serves an outdated crewship-sidecar and is waiting to be recreated"
}

func isSidecarRefreshPending(err error) bool {
	var pending *sidecarRefreshPendingError
	return errors.As(err, &pending)
}

type staleSidecarToleratedKey struct{}

// withStaleSidecarTolerated marks a reconcile that must reuse a running
// runtime with an outdated sidecar instead of asking for its replacement.
func withStaleSidecarTolerated(ctx context.Context) context.Context {
	return context.WithValue(ctx, staleSidecarToleratedKey{}, true)
}

// sidecarRefreshRequested reports whether reconcile may ask for a running
// runtime's replacement: only under a runtime-use reservation, and only once
// per acquisition.
func sidecarRefreshRequested(ctx context.Context) bool {
	managed, _ := ctx.Value(managedRuntimeUseKey{}).(bool)
	tolerated, _ := ctx.Value(staleSidecarToleratedKey{}).(bool)
	return managed && !tolerated
}

// SetSidecarRecreatedHook installs the observer told about every runtime this
// provider replaced because of an outdated sidecar. The server journals it as
// sidecar.recreated.
func (p *Provider) SetSidecarRecreatedHook(fn func(context.Context, provider.SidecarRecreatedEvent)) {
	p.sidecarHookMu.Lock()
	p.sidecarRecreatedHook = fn
	p.sidecarHookMu.Unlock()
}

// noteSidecarRecreate remembers, under the crew lock, that the runtime just
// removed is being replaced because of its sidecar. reportSidecarRecreate
// completes and publishes it once the replacement has started.
func (p *Provider) noteSidecarRecreate(team provider.CrewConfig, previousID, previousHash, hash string) {
	p.sidecarRecreates.Store(team.ID, provider.SidecarRecreatedEvent{
		CrewID:              team.ID,
		CrewSlug:            team.Slug,
		PreviousContainerID: previousID,
		PreviousSidecarHash: previousHash,
		SidecarHash:         hash,
	})
}

func (p *Provider) reportSidecarRecreate(ctx context.Context, crewID, containerID string) {
	v, ok := p.sidecarRecreates.LoadAndDelete(crewID)
	if !ok {
		return
	}
	ev := v.(provider.SidecarRecreatedEvent)
	ev.ContainerID = containerID
	p.logger.Info("crew container recreated to pick up the current crewship-sidecar",
		"crew_id", ev.CrewID,
		"previous_container", provider.ShortID(ev.PreviousContainerID),
		"container", provider.ShortID(containerID),
		"previous_sidecar_hash", ev.PreviousSidecarHash,
		"sidecar_hash", ev.SidecarHash,
	)
	p.sidecarHookMu.RLock()
	hook := p.sidecarRecreatedHook
	p.sidecarHookMu.RUnlock()
	if hook != nil {
		hook(ctx, ev)
	}
}

// recycleStaleSidecarRuntime is AcquireCrewRuntimeUse's answer to a running
// runtime with an outdated sidecar. It stops the runtime only when it can take
// the crew's gate exclusively without waiting AND the controller confirms the
// runtime is idle; the following reconcile then recreates the stopped
// container. Whatever happens, the returned context tolerates the outdated
// sidecar, so the acquisition can never loop and a busy runtime is reused.
func (p *Provider) recycleStaleSidecarRuntime(ctx context.Context, cfg provider.CrewConfig, pending *sidecarRefreshPendingError) context.Context {
	tolerated := withStaleSidecarTolerated(ctx)
	keep := func(reason string, err error) context.Context {
		p.logger.Info("crew container serves an outdated crewship-sidecar but is not idle; reusing it until its next idle start",
			"crew_id", cfg.ID,
			"container", provider.ShortID(pending.ContainerID),
			"container_sidecar_hash", pending.CurrentHash,
			"sidecar_hash", pending.DesiredHash,
			"reason", reason,
			"error", err,
		)
		return tolerated
	}
	// A synchronous child of a run that already holds the runtime: the parent
	// is by definition still using it.
	if provider.RuntimeUseNoWait(ctx) {
		return keep("nested use", nil)
	}
	unlock, ok := p.runtimeUseGate(cfg.ID).TryExclusive()
	if !ok {
		return keep("runtime in use", nil)
	}
	defer unlock()
	if err := p.verifyUnusedCrewRuntime(ctx, cfg.ID, pending.ContainerID); err != nil {
		return keep("idle not confirmed", err)
	}
	if err := p.StopCrewRuntime(ctx, pending.ContainerID); err != nil {
		return keep("stop failed", err)
	}
	return tolerated
}
