package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

// confirmLegacyRunStop never treats a tmux acknowledgement as proof that the
// original provider exec exited. Both identities must be confirmed under one
// bounded context, before capacity or credentials may be released.
func (o *Orchestrator) confirmLegacyRunStop(ctx context.Context, location RunLocation, execID string) (bool, int, error) {
	if execID == "" {
		return false, 0, errors.New("cancelled execution has no inspectable ID")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		stopped, code, err := o.legacyRunStoppedOnce(ctx, location, execID)
		if err != nil || stopped {
			return stopped, code, err
		}
		select {
		case <-ctx.Done():
			return false, 0, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (o *Orchestrator) legacyRunStoppedOnce(ctx context.Context, location RunLocation, execID string) (bool, int, error) {
	if execID == "" {
		return false, 0, errors.New("cancelled execution has no inspectable ID")
	}
	stopped, err := o.StopRunAt(ctx, location)
	if err != nil || !stopped {
		return false, 0, err
	}
	running, code, err := o.container.ExecInspect(ctx, execID)
	if err != nil {
		return false, 0, fmt.Errorf("inspect cancelled execution: %w", err)
	}
	return !running, code, nil
}

// Protected capture owns the underlying process tree and its final result.
// Stopping an observer or seeing the wrapper exit cannot substitute for that
// exact retained terminal record.
func (o *Orchestrator) confirmProtectedRunStop(ctx context.Context, location RunLocation, run RunState) (runoutput.Snapshot, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		stopped, err := o.StopRunAt(ctx, location)
		lastErr = err
		if stopped {
			snapshot, readErr := o.ReadRetainedRunResult(ctx, run)
			if readErr == nil && snapshot.Complete && snapshot.Result.Reason == "exited" {
				return snapshot, nil
			}
		}
		select {
		case <-ctx.Done():
			return runoutput.Snapshot{}, errors.Join(ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}
