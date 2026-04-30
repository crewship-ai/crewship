package orchestrator

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/containerstate"
)

// recordContainerSnapshot probes the container's actual installed package
// state (apt + pip + npm + os-release) and emits a container.snapshot
// journal entry — but only when the resulting hash differs from the last
// snapshot for the same container. This makes the journal a real "what
// changed" log instead of a noisy heartbeat: on a quiet session that
// installed nothing, no entry is written.
//
// Best-effort: every failure is swallowed (debug-logged). Failing the
// probe must never block agent run completion — a missing snapshot is
// strictly less bad than a successful run that crewshipd reports as
// failed because the post-hook panicked.
func (o *Orchestrator) recordContainerSnapshot(ctx context.Context, req AgentRunRequest, containerID string) {
	if containerID == "" || o.container == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			o.logger.Debug("container snapshot probe panicked", "container_id", containerID, "panic", r)
		}
	}()

	snap, err := containerstate.Capture(ctx, o.container, containerID)
	if err != nil {
		o.logger.Debug("container snapshot capture failed", "container_id", containerID, "error", err)
		return
	}

	hash := snap.Hash()
	o.snapshotHashMu.Lock()
	prev, seen := o.snapshotHashCache[containerID]
	if seen && prev == hash {
		o.snapshotHashMu.Unlock()
		return
	}
	o.snapshotHashCache[containerID] = hash
	o.snapshotHashMu.Unlock()

	payload := map[string]any{
		"hash":          hash,
		"apt":           snap.APT,
		"pip":           snap.Pip,
		"npm":           snap.Npm,
		"os":            snap.OS,
		"errs":          snap.Errs,
		"counts": map[string]int{
			"apt": len(snap.APT),
			"pip": len(snap.Pip),
			"npm": len(snap.Npm),
		},
	}
	summary := fmt.Sprintf("crew %s container snapshot: %d apt + %d pip + %d npm",
		req.CrewSlug, len(snap.APT), len(snap.Pip), len(snap.Npm))

	_, _ = o.getJournal().Emit(ctx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		MissionID:   req.MissionID,
		Type:        "container.snapshot",
		Severity:    "info",
		ActorType:   "system",
		ActorID:     containerID,
		Summary:     summary,
		Payload:     payload,
		Refs:        map[string]any{"chat_id": req.ChatID, "container_id": containerID},
	})
}
