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
// two chats). They share the same /secrets/<slug> files, so removal is
// refcounted per container+agent — only the last finisher cleans up. A run
// whose CLI exec is still alive when RunAgent returns (detached tmux session)
// keeps its hold forever; that fails safe (files persist inside the tmpfs
// until container stop, exactly the pre-change behaviour).

import (
	"context"
	"io"
	"regexp"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// agentSlugSafeRE mirrors the reconciler's slug validator
// (internal/api/credential_reconcile.go credSlugRE): the slug lands inside a
// shell `rm -rf`, so re-check the charset even though slugs are validated at
// creation.
var agentSlugSafeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const secretsCleanupTimeout = 10 * time.Second

// buildSecretsCleanupScript emits the `sh -c` body that removes an agent's
// per-run secret files. Returns "" for a slug that fails the safety charset —
// the caller must then skip the exec entirely.
func buildSecretsCleanupScript(agentSlug string) string {
	if !agentSlugSafeRE.MatchString(agentSlug) {
		return ""
	}
	return "rm -rf '/secrets/" + agentSlug + "'"
}

// hasFileMountedCreds reports whether any credential in the run request will
// be materialized on disk by buildCredFileScript — i.e. whether there is
// anything for the post-run cleanup to remove. Mirrors buildCredFileScript's
// skip conditions (empty env var / empty value / sidecar-injected types).
func hasFileMountedCreds(creds []Credential) bool {
	for _, c := range creds {
		if c.EnvVarName == "" || c.PlainValue == "" {
			continue
		}
		switch c.Type {
		case "CLI_TOKEN", "SECRET", "GENERIC_SECRET", "USERPASS", "SSH_KEY", "CERTIFICATE":
			return true
		}
	}
	return false
}

func secretsHoldKey(containerID, agentSlug string) string {
	return containerID + "|" + agentSlug
}

// retainAgentSecrets records that a live run is using /secrets/<slug> in the
// given container. Must be paired with releaseAgentSecrets.
func (o *Orchestrator) retainAgentSecrets(containerID, agentSlug string) {
	o.secretsHoldsMu.Lock()
	defer o.secretsHoldsMu.Unlock()
	if o.secretsHolds == nil {
		o.secretsHolds = make(map[string]int)
	}
	o.secretsHolds[secretsHoldKey(containerID, agentSlug)]++
}

// releaseAgentSecrets drops one hold and reports whether the caller is the
// last holder (and therefore responsible for the cleanup exec).
func (o *Orchestrator) releaseAgentSecrets(containerID, agentSlug string) bool {
	o.secretsHoldsMu.Lock()
	defer o.secretsHoldsMu.Unlock()
	key := secretsHoldKey(containerID, agentSlug)
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

// cleanupAgentSecrets removes /secrets/<agentSlug> from the container,
// exec'd as the agent UID. Best-effort with its own bounded context (the
// run's ctx may already be cancelled when this fires): a stopped container
// or failed exec is logged, never surfaced — the next run rewrites the files
// regardless, and the tmpfs mount guarantees they die with the container.
func (o *Orchestrator) cleanupAgentSecrets(containerID, agentSlug string) {
	if o.container == nil || containerID == "" {
		return
	}
	script := buildSecretsCleanupScript(agentSlug)
	if script == "" {
		o.logger.Warn("post-run secrets cleanup skipped: unsafe agent slug", "agent_slug", agentSlug)
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
		o.logger.Debug("post-run secrets cleanup exec skipped", "agent_slug", agentSlug, "error", err)
		return
	}
	if res != nil && res.Reader != nil {
		_, _ = io.Copy(io.Discard, res.Reader)
		_ = res.Reader.Close()
	}
	if res != nil {
		if running, code, ierr := o.container.ExecInspect(ctx, res.ExecID); ierr == nil && !running && code != 0 {
			o.logger.Warn("post-run secrets cleanup exited non-zero",
				"agent_slug", agentSlug, "exit_code", code)
			return
		}
	}
	o.logger.Debug("post-run secrets cleanup done", "agent_slug", agentSlug)
}
