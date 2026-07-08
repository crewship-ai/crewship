package api

// Revocation → running-container reconciliation (#814).
//
// Non-LLM credentials (DB passwords, GH_TOKEN, SSH keys, certs) are
// materialized as files under /secrets/{agent-slug}/ at agent-run boot
// (internal/orchestrator/exec_sidecar.go buildCredFileScript), owned by the
// agent UID (1001) in a 0700 dir. They are never re-reconciled, so a revoked
// file stays readable to the agent for the container's whole lifetime.
//
// The sidecar (UID 1002) cannot remove them — the dir is 0700/1001. So the
// removal runs SERVER-SIDE: on revoke we `docker exec` into the crew's
// running container AS UID 1001 (the only principal that can unlink inside
// that dir) and `rm -f` the secret file(s). This mirrors the exec-as-1001
// path the keeper already uses (internal/api/keeper_execute.go).
//
// Best-effort by design: a stopped/absent container or a failed exec is
// logged, never fatal — the credential is already revoked in the DB (its
// deleted_at is set), so it will not be re-materialized on the next boot;
// this pass only closes the window for containers running at revoke time.

import (
	"context"
	"io"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Defensive validators — env var names and agent slugs both come from our own
// DB (validated at creation), but the values land in a shell `rm` command, so
// re-check the charset and single-quote the paths regardless.
var (
	credEnvVarRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	credSlugRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// credSecretPaths returns the absolute in-container paths a file-mounted
// credential occupies, mirroring buildCredFileScript's per-type layout
// (internal/orchestrator/exec_sidecar.go). API_KEY / AI_CLI_TOKEN / OAUTH2 and
// unknown types return nil — those never touch disk (sidecar-injected).
func credSecretPaths(agentSlug, envVar, credType string) []string {
	dir := "/secrets/" + agentSlug
	switch credType {
	case "SSH_KEY":
		return []string{dir + "/ssh/" + envVar}
	case "CERTIFICATE":
		return []string{dir + "/certs/" + envVar + ".pem"}
	case "USERPASS":
		return []string{dir + "/" + envVar + "_USERNAME", dir + "/" + envVar + "_PASSWORD"}
	case "CLI_TOKEN", "SECRET", "GENERIC_SECRET":
		return []string{dir + "/" + envVar}
	default:
		return nil
	}
}

// buildCredRemoveScript emits the `sh -c` body that removes a credential's
// file(s) from a running container. Paths are single-quoted (the segments are
// validated safe by the caller). Returns "" when the type has no on-disk form.
//
// The .env hint-map (envvar → path) is intentionally left alone: the agent
// reads secrets by path and .env is advisory, so a now-dangling entry is inert
// (the file it points at is gone) and rewriting a 0400 file adds shell/portability
// risk for no security gain. It clears on the next container boot.
func buildCredRemoveScript(agentSlug, envVar, credType string) string {
	paths := credSecretPaths(agentSlug, envVar, credType)
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("rm -f")
	for _, p := range paths {
		b.WriteString(" '")
		b.WriteString(p)
		b.WriteString("'")
	}
	return b.String()
}

// reconcileRevokedCredential removes a just-revoked credential's materialized
// /secrets files from every running crew container that holds it, exec'd as
// UID 1001. Best-effort — see the package doc. No-op when the container
// provider isn't wired (tests / --no-docker).
func (h *CredentialHandler) reconcileRevokedCredential(ctx context.Context, credentialID, workspaceID string) {
	if h.container == nil {
		return
	}

	// Which agents hold this credential, and where. Whether it lives on disk
	// is decided by the credential TYPE (credSecretPaths, mirroring the boot
	// materializer exec_sidecar.go), NOT by agent_credentials.mount_type —
	// that column is vestigial: migration v94 adds it DEFAULT 'env' but no
	// code path ever sets it to 'file', so filtering on it here would match
	// nothing and remove nothing. Non-file types (API_KEY/AI_CLI_TOKEN/OAUTH2)
	// fall out below when credSecretPaths returns no paths. Only live agents
	// in live crews have a running container to reach.
	rows, err := h.db.QueryContext(ctx, `
		SELECT a.slug, cr.id, cr.slug, ac.env_var_name, c.type
		FROM agent_credentials ac
		JOIN agents a       ON a.id = ac.agent_id AND a.deleted_at IS NULL
		JOIN credentials c  ON c.id = ac.credential_id
		JOIN crews cr       ON cr.id = a.crew_id AND cr.deleted_at IS NULL
		WHERE ac.credential_id = ? AND c.workspace_id = ?`,
		credentialID, workspaceID)
	if err != nil {
		h.logger.Warn("revoke reconcile: query file mounts", "credential_id", credentialID, "error", err)
		return
	}
	defer rows.Close()

	type target struct{ agentSlug, crewID, crewSlug, envVar, credType string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.agentSlug, &t.crewID, &t.crewSlug, &t.envVar, &t.credType); err != nil {
			h.logger.Warn("revoke reconcile: scan", "error", err)
			return
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		h.logger.Warn("revoke reconcile: rows", "error", err)
		return
	}

	for _, t := range targets {
		if !credSlugRE.MatchString(t.agentSlug) || !credEnvVarRE.MatchString(t.envVar) {
			h.logger.Warn("revoke reconcile: skipping unsafe identifiers",
				"agent_slug", t.agentSlug, "env_var", t.envVar)
			continue
		}
		script := buildCredRemoveScript(t.agentSlug, t.envVar, t.credType)
		if script == "" {
			continue // type has no on-disk form
		}
		container := h.container.CrewContainerName(t.crewID, t.crewSlug)
		res, execErr := h.container.Exec(ctx, provider.ExecConfig{
			ContainerID: container,
			Cmd:         []string{"sh", "-c", script},
			User:        "1001:1001",
		})
		if execErr != nil {
			// Overwhelmingly "container not running" — expected and benign
			// (nothing to remove; won't re-materialize post-revoke).
			h.logger.Debug("revoke reconcile: exec skipped (container likely stopped)",
				"credential_id", credentialID, "crew_id", t.crewID, "error", execErr)
			continue
		}
		if res != nil && res.Reader != nil {
			_, _ = io.Copy(io.Discard, res.Reader)
			_ = res.Reader.Close()
		}
		if res != nil {
			if running, code, ierr := h.container.ExecInspect(ctx, res.ExecID); ierr == nil && !running && code != 0 {
				h.logger.Warn("revoke reconcile: rm exited non-zero",
					"credential_id", credentialID, "crew_id", t.crewID, "agent_slug", t.agentSlug, "exit_code", code)
			}
		}
	}
}
