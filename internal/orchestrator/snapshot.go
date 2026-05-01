package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/containerstate"
)

// snapshotProbeTimeout bounds the four exec calls that capture the
// container's actual installed-package state. Three execs of dpkg-query
// / pip / npm against a healthy container finish in well under a
// second; capping at 30s means a hung probe (broken binary, frozen
// container) can't block the run-completion path indefinitely.
//
// `var` not `const` so a test can override it with a sub-second value
// to exercise the hang-resilience path without adding 30s to the suite.
var snapshotProbeTimeout = 30 * time.Second

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

	// Decouple from the request ctx so a user cancelling chat right at
	// run-end doesn't cancel the snapshot probe (snapshot survives the
	// "I clicked Stop" case). The probe still has a hard upper bound so
	// a hung dpkg-query / npm ls can't wedge run completion.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotProbeTimeout)
	defer cancel()

	snap, err := containerstate.Capture(probeCtx, o.container, containerID)
	if err != nil {
		o.logger.Debug("container snapshot capture failed", "container_id", containerID, "error", err)
		return
	}

	hash := snap.Hash()
	// Atomic claim under the lock: skip if (a) an identical snapshot was
	// already emitted, or (b) another goroutine already claimed the emit
	// slot for this container — a single in-flight emit dedupes concurrent
	// callers since the resulting journal entry will satisfy them all.
	o.snapshotHashMu.Lock()
	if prev, seen := o.snapshotHashCache[containerID]; seen && prev == hash {
		o.snapshotHashMu.Unlock()
		return
	}
	if o.snapshotPending[containerID] {
		o.snapshotHashMu.Unlock()
		return
	}
	o.snapshotPending[containerID] = true
	o.snapshotHashMu.Unlock()
	defer func() {
		// Unconditionally release the pending claim. The hash itself
		// is published via snapshotHashCache only on a successful emit
		// (see below), so a failure here leaves no entry behind and
		// the next caller retries — preserving the existing
		// "failed-emit doesn't poison the dedup cache" invariant.
		o.snapshotHashMu.Lock()
		delete(o.snapshotPending, containerID)
		o.snapshotHashMu.Unlock()
	}()

	payload := map[string]any{
		"hash": hash,
		"apt":  snap.APT,
		"pip":  snap.Pip,
		"npm":  snap.Npm,
		"os":   snap.OS,
		"errs": snap.Errs,
		"counts": map[string]int{
			"apt": len(snap.APT),
			"pip": len(snap.Pip),
			"npm": len(snap.Npm),
		},
	}
	summary := fmt.Sprintf("crew %s container snapshot: %d apt + %d pip + %d npm",
		req.CrewSlug, len(snap.APT), len(snap.Pip), len(snap.Npm))

	// Emit with the same uncancelled bounded ctx as the probe — a caller
	// cancelling chat at end-of-run must not stop the journal write
	// either, otherwise the next agent run finds a stale prev-hash and
	// re-attempts the same emit.
	emitCtx, emitCancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotProbeTimeout)
	defer emitCancel()

	if _, err := o.getJournal().Emit(emitCtx, JournalEntry{
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
	}); err != nil {
		// Don't poison the dedup cache: leaving prev (or absence) intact
		// means the next run with the same state retries the emit. A
		// successful write is the only signal that this snapshot really
		// reached the journal.
		o.logger.Debug("container snapshot emit failed",
			"container_id", containerID, "error", err)
		return
	}

	// Emit succeeded — now safe to record the hash so subsequent runs
	// with identical state can short-circuit.
	o.snapshotHashMu.Lock()
	o.snapshotHashCache[containerID] = hash
	o.snapshotHashMu.Unlock()
}
