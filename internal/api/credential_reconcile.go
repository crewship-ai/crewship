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
	"database/sql"
	"io"
	"log/slog"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/credname"
	"github.com/crewship-ai/crewship/internal/provider"
)

// Defensive validator — an agent slug comes from our own DB (validated at
// creation), but the value lands in a shell `rm` command, so re-check the
// charset and single-quote the paths regardless.
//
// The env-var half of this pair used to be `^[A-Za-z_][A-Za-z0-9_]*$` — mixed
// case legal, unlike the writer it is supposed to mirror (#1657). It is now
// credname, and the difference is not cosmetic: this file removes files that
// buildCredFileScript WROTE, so it has to spell the name the way delivery did.
// A revoke that looks for /secrets/<agent>/gh_token while delivery wrote
// /secrets/<agent>/GH_TOKEN removes nothing on a case-sensitive filesystem and,
// being a best-effort `rm -f`, reports success — the operator believes a live
// container no longer holds a secret it is still reading.
var credSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// credSecretPaths returns the absolute in-container paths a file-mounted
// credential occupies, mirroring buildCredFileScript's per-type layout
// (internal/orchestrator/exec_sidecar.go). API_KEY / AI_CLI_TOKEN / OAUTH2 and
// unknown types return nil — those never touch disk (sidecar-injected).
//
// fieldKeys are the credential's multi-part field keys (PRD §2.2). Each becomes
// one more flat file, named by deliveredFieldEnvVar — the SAME function the
// delivery path used to write it, because a revoke that spells the name its own
// way removes nothing and, being a best-effort `rm -f`, reports success anyway.
// Without this a revoked AWS credential would leave its secret access key on
// disk in a live container while the vault showed it gone.
//
// A key whose derived name fails the charset check is skipped rather than
// interpolated: delivery refused to write that file for the same reason, so
// there is nothing to remove, and the one outcome that matters is that it never
// reaches the shell.
func credSecretPaths(agentSlug, envVar, credType string, fieldKeys []string) []string {
	dir := "/secrets/" + agentSlug
	var paths []string
	switch credType {
	case "SSH_KEY":
		paths = []string{dir + "/ssh/" + envVar}
	case "CERTIFICATE":
		paths = []string{dir + "/certs/" + envVar + ".pem"}
	case "USERPASS":
		paths = []string{dir + "/" + envVar + "_USERNAME", dir + "/" + envVar + "_PASSWORD"}
	case "CLI_TOKEN", "SECRET", "GENERIC_SECRET":
		paths = []string{dir + "/" + envVar}
	default:
		// The credential itself never touches disk, so neither do its parts —
		// buildCredFileScript skips the whole credential before reaching them.
		return nil
	}
	for _, key := range fieldKeys {
		name := deliveredFieldEnvVar(envVar, key)
		if !credname.Valid(name) {
			continue
		}
		paths = append(paths, dir+"/"+name)
	}
	return paths
}

// buildCredRemoveScript emits the `sh -c` body that removes a credential's
// file(s) from a running container. Paths are single-quoted (the segments are
// validated safe by the caller). Returns "" when the type has no on-disk form.
//
// The .env hint-map (envvar → path) is intentionally left alone: the agent
// reads secrets by path and .env is advisory, so a now-dangling entry is inert
// (the file it points at is gone) and rewriting a 0400 file adds shell/portability
// risk for no security gain. It clears on the next container boot.
func buildCredRemoveScript(agentSlug, envVar, credType string, fieldKeys []string) string {
	paths := credSecretPaths(agentSlug, envVar, credType, fieldKeys)
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
	reconcileRevokedCredentialFiles(ctx, h.db, h.logger, h.container, credentialID, workspaceID)
}

// reconcileRevokedCredentialFiles is the handler-independent core shared by
// the public DELETE handler (CredentialHandler) and the internal status
// endpoint (InternalHandler — a sidecar-observed REVOKED must remove files
// just like an operator delete). workspaceID may be empty (the internal
// caller doesn't always send it); it is then resolved from the credential row.
func reconcileRevokedCredentialFiles(ctx context.Context, db *sql.DB, logger *slog.Logger, ctr provider.ContainerProvider, credentialID, workspaceID string) {
	if ctr == nil {
		return
	}
	if workspaceID == "" {
		if err := db.QueryRowContext(ctx,
			`SELECT workspace_id FROM credentials WHERE id = ?`, credentialID).Scan(&workspaceID); err != nil {
			logger.Warn("revoke reconcile: resolve workspace", "credential_id", credentialID, "error", err)
			return
		}
	}

	// Which agents hold this credential, and where. Whether it lives on disk
	// is decided by the credential TYPE (credSecretPaths, mirroring the boot
	// materializer exec_sidecar.go), NOT by agent_credentials.mount_type —
	// that column is vestigial: migration v94 adds it DEFAULT 'env' but no
	// code path ever sets it to 'file', so filtering on it here would match
	// nothing and remove nothing. Non-file types (API_KEY/AI_CLI_TOKEN/OAUTH2)
	// fall out below when credSecretPaths returns no paths. Only live agents
	// in live crews have a running container to reach.
	rows, err := db.QueryContext(ctx, `
		SELECT a.slug, cr.id, cr.slug, ac.env_var_name, c.type
		FROM agent_credentials ac
		JOIN agents a       ON a.id = ac.agent_id AND a.deleted_at IS NULL
		JOIN credentials c  ON c.id = ac.credential_id
		JOIN crews cr       ON cr.id = a.crew_id AND cr.deleted_at IS NULL
		WHERE ac.credential_id = ? AND c.workspace_id = ?`,
		credentialID, workspaceID)
	if err != nil {
		logger.Warn("revoke reconcile: query file mounts", "credential_id", credentialID, "error", err)
		return
	}
	defer rows.Close()

	type target struct{ agentSlug, crewID, crewSlug, envVar, credType string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.agentSlug, &t.crewID, &t.crewSlug, &t.envVar, &t.credType); err != nil {
			logger.Warn("revoke reconcile: scan", "error", err)
			return
		}
		targets = append(targets, t)
	}
	if err := rows.Err(); err != nil {
		logger.Warn("revoke reconcile: rows", "error", err)
		return
	}

	// The credential's multi-part field keys (PRD §2.2), read once for all
	// targets — they belong to the credential, not to the grant, so the same
	// list applies to every agent holding it. Only the KEYS are read; the
	// values are irrelevant to a delete and there is no reason to pull a
	// secret's ciphertext into this process to build an `rm`.
	//
	// A read failure means an incomplete removal, which is exactly the case that
	// must be loud: it is the difference between "the secret is gone from the
	// container" and "the operator believes it is". The primary file is still
	// removed below — a partial revoke beats none.
	var fieldKeys []string
	if fieldRows, ferr := db.QueryContext(ctx,
		`SELECT key FROM credential_fields WHERE credential_id = ? ORDER BY ordinal ASC, key ASC`,
		credentialID); ferr != nil {
		logger.Warn("revoke reconcile: field keys — multi-part files may survive in running containers",
			"credential_id", credentialID, "error", ferr)
	} else {
		for fieldRows.Next() {
			var key string
			if serr := fieldRows.Scan(&key); serr != nil {
				logger.Warn("revoke reconcile: scan field key", "credential_id", credentialID, "error", serr)
				break
			}
			fieldKeys = append(fieldKeys, key)
		}
		if ierr := fieldRows.Err(); ierr != nil {
			logger.Warn("revoke reconcile: iterate field keys", "credential_id", credentialID, "error", ierr)
		}
		fieldRows.Close()
	}

	for _, t := range targets {
		// The stored env_var_name is normalised the SAME way the delivery path
		// normalises it (credential_slot_delivery.go), because the file on disk
		// carries the delivered name, not the stored one. Rows written before
		// #1657 hold whatever the assign endpoint accepted when it checked
		// nothing but non-emptiness; those are exactly the rows whose files
		// would otherwise survive a revoke.
		envVar, ok := credname.Canonical(t.envVar)
		if !credSlugRE.MatchString(t.agentSlug) || !ok {
			logger.Warn("revoke reconcile: skipping unsafe identifiers",
				"agent_slug", t.agentSlug, "env_var", t.envVar)
			continue
		}
		t.envVar = envVar
		script := buildCredRemoveScript(t.agentSlug, t.envVar, t.credType, fieldKeys)
		if script == "" {
			continue // type has no on-disk form
		}
		container := ctr.CrewContainerName(t.crewID, t.crewSlug)
		res, execErr := ctr.Exec(ctx, provider.ExecConfig{
			ContainerID: container,
			Cmd:         []string{"sh", "-c", script},
			User:        "1001:1001",
		})
		if execErr != nil {
			// Overwhelmingly "container not running" — expected and benign
			// (nothing to remove; won't re-materialize post-revoke).
			logger.Debug("revoke reconcile: exec skipped (container likely stopped)",
				"credential_id", credentialID, "crew_id", t.crewID, "error", execErr)
			continue
		}
		if res != nil && res.Reader != nil {
			_, _ = io.Copy(io.Discard, res.Reader)
			_ = res.Reader.Close()
		}
		if res != nil {
			if running, code, ierr := ctr.ExecInspect(ctx, res.ExecID); ierr == nil && !running && code != 0 {
				logger.Warn("revoke reconcile: rm exited non-zero",
					"credential_id", credentialID, "crew_id", t.crewID, "agent_slug", t.agentSlug, "exit_code", code)
			}
		}
	}
}
