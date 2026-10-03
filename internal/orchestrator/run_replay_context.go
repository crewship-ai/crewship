package orchestrator

import (
	"context"
	"errors"
)

// ReplayContextStore preserves host-only scrub literals across credential
// rotation. Implementations must encrypt at rest and bind both scope IDs.
// Missing context is an error, distinct from a known empty list of secrets.
type ReplayContextStore interface {
	Save(context.Context, string, string, []string) error
	Load(context.Context, string, string) ([]string, error)
}

var ErrReplayContextUnavailable = errors.New("durable run requires persisted replay scrub context")

func (o *Orchestrator) SetReplayContextStore(store ReplayContextStore) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.replayContextStore = store
}

func (o *Orchestrator) saveReplayContext(ctx context.Context, req AgentRunRequest, values []string) error {
	o.mu.RLock()
	store := o.replayContextStore
	o.mu.RUnlock()
	if store == nil || store.Save(ctx, req.WorkspaceID, req.RunID, values) != nil {
		// Store/provider errors may contain SQL arguments or secret data. They
		// must not enter the ordinary run-failure journal or user-visible output.
		return ErrReplayContextUnavailable
	}
	return nil
}
