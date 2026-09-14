package orchestrator

// Post-run secret cleanup (secret lifecycle hardening, A3).
//
// writeCredentialFiles materializes file-mounted credentials under
// /secrets/<agent-slug>/ at EVERY run setup, so nothing depends on the files
// surviving between runs — but until now they did, staying readable to any
// process in the container for its whole lifetime. After a run finishes we
// remove the agent's whole /secrets/<slug> directory (exec'd as UID 1001,
// the only principal that can unlink inside the 0700 dir under CapDrop=ALL).
//
// Concurrency: multiple runs of the same agent may overlap (chat + routine,
// two chats). The refcount below is keyed per container+agent+RUN (E0). It was
// keyed per container+agent, which was correct for the question it was asked —
// "may these files be deleted yet?" — and could not answer the question that
// actually mattered: overlapping runs SHARED one /secrets/<slug> file set, so
// run B's writeCredentialFiles overwrote run A's credentials in place and A
// carried on using B's. The refcount stopped the files being deleted too
// early; nothing stopped them being replaced.
//
// Per-run directories (agentSecretsDir) fix the content collision, and keying
// the holds by run makes each run the sole holder of its own directory — so a
// finisher removes what it wrote and nothing else. The mechanism is unchanged
// and deliberately so; only its key got a third component. A run whose CLI
// exec is still alive when RunAgent returns (detached tmux session) keeps its
// hold forever; that fails safe (files persist inside the tmpfs until
// container stop, exactly the pre-change behaviour).
//
// Two TOCTOU windows are closed explicitly:
//
//  1. Retain happens BEFORE the credential write (orchestrator_run.go). If it
//     came after, a finishing run of the same agent could hit count→0 and rm
//     the files the starting run just wrote.
//  2. The last-holder decision and the rm exec are not atomic — the exec can
//     lag seconds behind. cleanupAgentSecrets therefore serializes with the
//     credential write via a per-key mutex (agentSecretsLock) AND re-checks
//     the hold count under that lock before running the rm: a run that
//     retained meanwhile vetoes the cleanup, and a run that retains during
//     the rm blocks on the lock until the rm finishes, then writes fresh
//     files after it.

import (
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/credpolicy"
	"github.com/crewship-ai/crewship/internal/provider"
)

// agentSlugSafeRE mirrors the reconciler's slug validator
// (internal/api/credential_reconcile.go credSlugRE): the slug lands inside a
// shell `rm -rf`, so re-check the charset even though slugs are validated at
// creation.
var agentSlugSafeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const secretsCleanupTimeout = 10 * time.Second

// buildSecretsCleanupScript emits the `sh -c` body that removes ONE RUN's
// secret files. Returns "" for a slug or run id that fails the safety charset
// — the caller must then skip the exec entirely.
//
// It removes /secrets/<slug>/<runID>, not /secrets/<slug>. Removing the whole
// agent directory is what it used to do, and after E0 made the directory
// per-run that would delete a concurrently-live sibling run's credentials out
// from under it — turning a cleanup into the very cross-run interference this
// package exists to prevent. The now-empty /secrets/<slug> parent is left
// behind deliberately: it is a directory, not a secret, and the next run of
// this agent needs it anyway.
func buildSecretsCleanupScript(agentSlug, runID string) string {
	if !agentSlugSafeRE.MatchString(agentSlug) || !ValidRunID(runID) {
		return ""
	}
	return "rm -rf '/secrets/" + agentSlug + "/" + runID + "'"
}

// hasFileMountedCreds reports whether any credential in the run request will
// be materialized on disk by buildCredFileScript — i.e. whether there is
// anything for the post-run cleanup to remove. Mirrors buildCredFileScript's
// skip conditions (empty env var / empty value / sidecar-injected types) AND
// its keeperEnabled gate: SECRET is file-mounted only when Keeper is OFF, so
// with Keeper ON a run whose only file-typed creds are SECRETs writes nothing
// and needs no cleanup/lock. Keep this in lockstep with buildCredFileScript.
func hasFileMountedCreds(creds []Credential, keeperEnabled bool) bool {
	for _, c := range creds {
		if c.EnvVarName == "" || c.PlainValue == "" {
			continue
		}
		pol := credpolicy.For(c.Type)
		if !pol.FileMounted() {
			continue // not written to /secrets (proxy/env-only or unknown)
		}
		if pol.KeeperGated && keeperEnabled {
			continue // withheld under Keeper — nothing written, nothing to clean
		}
		return true
	}
	return false
}

func secretsHoldKey(containerID, agentSlug, runID string) string {
	return containerID + "|" + agentSlug + "|" + runID
}

// retainAgentSecrets records that a live run is using /secrets/<slug> in the
// given container. Must be paired with releaseAgentSecrets.
func (o *Orchestrator) retainAgentSecrets(containerID, agentSlug, runID string) {
	o.secretsHoldsMu.Lock()
	defer o.secretsHoldsMu.Unlock()
	if o.secretsHolds == nil {
		o.secretsHolds = make(map[string]int)
	}
	o.secretsHolds[secretsHoldKey(containerID, agentSlug, runID)]++
}

// releaseAgentSecrets drops one hold and reports whether the caller is the
// last holder (and therefore responsible for the cleanup exec).
func (o *Orchestrator) releaseAgentSecrets(containerID, agentSlug, runID string) bool {
	o.secretsHoldsMu.Lock()
	defer o.secretsHoldsMu.Unlock()
	key := secretsHoldKey(containerID, agentSlug, runID)
	if o.secretsHolds == nil {
		return true
	}
	if n := o.secretsHolds[key] - 1; n > 0 {
		o.secretsHolds[key] = n
		return false
	}
	delete(o.secretsHolds, key)
	return true
}

// secretsHoldCount returns the current number of live holds for the key.
func (o *Orchestrator) secretsHoldCount(containerID, agentSlug, runID string) int {
	o.secretsHoldsMu.Lock()
	defer o.secretsHoldsMu.Unlock()
	return o.secretsHolds[secretsHoldKey(containerID, agentSlug, runID)]
}

// agentSecretsLock returns the per-key mutex serializing the cleanup rm exec
// against credential writes for the same container+agent. Entries are small
// and bounded by #agents × #containers, so they are never evicted (evicting
// one while another goroutine still holds the lock would defeat the point).
func (o *Orchestrator) agentSecretsLock(containerID, agentSlug, runID string) *sync.Mutex {
	key := secretsHoldKey(containerID, agentSlug, runID)
	o.secretsHoldsMu.Lock()
	defer o.secretsHoldsMu.Unlock()
	if o.secretsKeyLocks == nil {
		o.secretsKeyLocks = make(map[string]*sync.Mutex)
	}
	lk, ok := o.secretsKeyLocks[key]
	if !ok {
		lk = &sync.Mutex{}
		o.secretsKeyLocks[key] = lk
	}
	return lk
}

// cleanupAgentSecrets removes /secrets/<agentSlug> from the container,
// exec'd as the agent UID. Best-effort with its own bounded context (the
// run's ctx may already be cancelled when this fires): a stopped container
// or failed exec is logged, never surfaced — the next run rewrites the files
// regardless, and the tmpfs mount guarantees they die with the container.
func (o *Orchestrator) cleanupAgentSecrets(containerID, agentSlug, runID string) {
	if o.container == nil || containerID == "" {
		return
	}
	script := buildSecretsCleanupScript(agentSlug, runID)
	if script == "" {
		o.logger.Warn("post-run secrets cleanup skipped: unsafe agent slug or run id",
			"agent_slug", agentSlug, "run_id", runID)
		return
	}
	// Serialize against credential writes and re-check the holds under the
	// lock (TOCTOU window #2, see the package doc): a run that retained
	// between our caller's last-holder decision and this point still needs
	// the files — skip; a run that retains after this check blocks on the
	// same lock until our rm finishes, then writes fresh files after it.
	lk := o.agentSecretsLock(containerID, agentSlug, runID)
	lk.Lock()
	defer lk.Unlock()
	if n := o.secretsHoldCount(containerID, agentSlug, runID); n > 0 {
		o.logger.Debug("post-run secrets cleanup skipped: run retained again",
			"agent_slug", agentSlug, "run_id", runID, "holds", n)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), secretsCleanupTimeout)
	defer cancel()
	res, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	})
	if err != nil {
		// Usually "container not running" — nothing left to remove then.
		o.logger.Debug("post-run secrets cleanup exec skipped", "agent_slug", agentSlug, "run_id", runID, "error", err)
		return
	}
	if res != nil && res.Reader != nil {
		_, _ = io.Copy(io.Discard, res.Reader)
		_ = res.Reader.Close()
	}
	if res != nil {
		running, code, ierr := o.container.ExecInspect(ctx, res.ExecID)
		switch {
		case ierr != nil:
			// The rm's own exit code is unknowable here — daemon hiccup,
			// exec id already reaped, whatever. Falling through to the
			// "done" log below would report a cleanup that may never have
			// happened as a verdict of success, in a path whose entire job
			// is making sure agent credential files do not outlive the run.
			// Say plainly that this is unknown rather than silently
			// upgrading it to "done".
			o.logger.Warn("post-run secrets cleanup outcome unknown: inspect failed",
				"agent_slug", agentSlug, "run_id", runID, "error", ierr)
			return
		case !running && code != 0:
			o.logger.Warn("post-run secrets cleanup exited non-zero",
				"agent_slug", agentSlug, "run_id", runID, "exit_code", code)
			return
		}
	}
	o.logger.Debug("post-run secrets cleanup done", "agent_slug", agentSlug, "run_id", runID)
}

// runHomeCleanupTimeout bounds the one rm that removes a finished run's HOME.
// Same order as secretsCleanupTimeout and for the same reason: this fires
// after the run, often against a container that is already going away.
const runHomeCleanupTimeout = 10 * time.Second

// buildRunHomeCleanupScript emits the `sh -c` body that removes one run's
// HOME. Returns "" for a slug or run id that fails the safety charset.
//
// The charset here is validSlugRe's, NOT agentSlugSafeRE's: the latter refuses
// a leading underscore, which the onboarding setup crew's slugs
// (_crewship-setup, _crewship-setup-guide) both carry. Reusing it would have
// meant those agents silently never cleaned up a run home, forever — a leak
// that only shows up on the one crew every new install runs. Both charsets
// exclude quotes, whitespace, "/" and shell metacharacters, which is the
// property that actually matters for an interpolated rm.
//
// `rm -rf` on a directory whose .memory is a SYMLINK removes the link, not the
// target: rm never follows a symlink to a directory. That is load-bearing —
// it is the whole reason .memory is symlinked rather than bind-mounted or
// copied — and runHomeCleanupRemovesTheLinkNotTheMemory pins it.
func buildRunHomeCleanupScript(agentSlug, runID string) string {
	if !validSlugRe.MatchString(agentSlug) || !ValidRunID(runID) {
		return ""
	}
	return "rm -rf '" + containerRunHomeRoot + "/" + agentSlug + "/" + runID + "'"
}

// cleanupRunHome removes /crew/runs/<slug>/<runID> from the container, exec'd
// as the agent UID. Best-effort with its own bounded context, exactly like
// cleanupAgentSecrets: the run's ctx is frequently already cancelled by the
// time this fires, and a stopped container has nothing left to remove.
func (o *Orchestrator) cleanupRunHome(containerID, agentSlug, runID, runToken string) {
	if o.container == nil || containerID == "" {
		return
	}
	script := buildRunHomeCleanupScript(agentSlug, runID)
	if script == "" {
		o.logger.Warn("post-run home cleanup skipped: unsafe agent slug or run id",
			"agent_slug", agentSlug, "run_id", runID)
		return
	}
	// Fold the sidecar's run-end notification into the SAME exec. It is the
	// other half of ending a run — after it, this run's token stops
	// authenticating (internal/sidecar/run_end.go) — and it happens at exactly
	// this moment, so paying a second exec round-trip for it would be waste.
	//
	// Ordering: notify FIRST, then remove the HOME. The reverse would delete
	// the directory a still-draining call might be reading from before telling
	// anyone the run was over.
	if notify := sidecarRunEndScript(runToken); notify != "" {
		script = notify + script
	}
	ctx, cancel := context.WithTimeout(context.Background(), runHomeCleanupTimeout)
	defer cancel()
	// Delivered on STDIN, not argv: the script carries this run's bearer token,
	// and a command line is readable by every other agent in the shared
	// container through /proc. Mirrors the preflight batch's delivery for the
	// same reason.
	res, err := o.container.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh"},
		Stdin:       strings.NewReader(script),
		User:        "1001:1001",
	})
	if err != nil {
		o.logger.Debug("post-run home cleanup exec skipped",
			"agent_slug", agentSlug, "run_id", runID, "error", err)
		return
	}
	if res != nil && res.Reader != nil {
		_, _ = io.Copy(io.Discard, res.Reader)
		_ = res.Reader.Close()
	}
	o.logger.Debug("post-run home cleanup done", "agent_slug", agentSlug, "run_id", runID)
}
