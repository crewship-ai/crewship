package docker

// One-time startup scrub of legacy host-side secrets dirs (secret lifecycle
// hardening). Before /secrets became a tmpfs (secretsTmpfsSpec), every crew
// persisted cleartext credential files under OutputBasePath/secrets/<crew-id>.
// EnsureCrewRuntime scrubs a crew's dir when that crew is (re)provisioned,
// but deleted and dormant crews never reach that path — their cleartext would
// sit on the host disk forever. This sweep runs once at provider
// construction:
//
//   - every subdirectory of OutputBasePath/secrets that is NOT still
//     bind-mounted into an existing container is removed;
//   - dirs still mounted into a (legacy, pre-tmpfs) container are skipped —
//     yanking a live bind mount's source out from under running agents would
//     break them. Those containers are recreated with the tmpfs on their next
//     EnsureCrewRuntime (the /secrets-drift check), which also removes the
//     host dir.
//
// Best-effort throughout: a failed list or removal is logged, never fatal —
// the worst case is the pre-existing exposure, and the next startup retries.
//
// A subtlety discovered in #1069: a dormant crew's per-agent subdirectory
// (OutputBasePath/secrets/<crew-id>/<agent-slug>) is created *inside* the
// container by writeCredentialFiles as UID 1001, mode 0700 (see
// internal/orchestrator/exec_sidecar.go). The crewship server process runs as
// a different, unprivileged UID with no CAP_DAC_OVERRIDE, so a plain
// os.RemoveAll fails to even open that subdirectory to read its contents —
// permission denied. removeLegacySecretsDirViaHelper below recovers from that
// by draining the subdirectory's contents from a short-lived helper container
// running as UID 1001 — the same mechanism writeCredentialFiles uses to WRITE
// those files in the first place. The helper only needs to delete the files;
// removing the (now empty) subdirectory entry itself only requires write
// permission on ITS PARENT, which the crewship server process already owns,
// so the caller's plain os.RemoveAll finishes the job on retry.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// legacySecretsHelperTimeout bounds how long the sweep waits for the UID-1001
// removal helper container to finish before giving up on that dir and
// falling back to the WARN-and-count-failed path.
const legacySecretsHelperTimeout = 30 * time.Second

// protectedLegacySecretsDirs returns the set of first-level directory names
// under secretsBase (i.e. crew IDs) that are still bind-mounted into some
// existing container and therefore must not be swept.
func protectedLegacySecretsDirs(containers []container.Summary, secretsBase string) map[string]bool {
	protected := make(map[string]bool)
	prefix := filepath.Clean(secretsBase) + string(filepath.Separator)
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Type != mount.TypeBind || m.Source == "" {
				continue
			}
			src := filepath.Clean(m.Source)
			if !strings.HasPrefix(src, prefix) {
				continue
			}
			rest := strings.TrimPrefix(src, prefix)
			if i := strings.IndexByte(rest, filepath.Separator); i >= 0 {
				rest = rest[:i]
			}
			if rest != "" {
				protected[rest] = true
			}
		}
	}
	return protected
}

// sweepLegacySecretsBase removes every first-level subdirectory of base whose
// name is not in protected. Returns removal/skip/failure counts for the
// caller's summary log. A missing base is a clean no-op.
//
// removeAll performs the plain host-side removal (production callers pass
// os.RemoveAll; tests inject a fake to simulate the permission-denied
// condition from #1069 without relying on real, CI-fragile chmod bits).
//
// removeStuck is invoked when removeAll fails; it should recover a dir whose
// contents are owned by the in-container UID 1001 (production callers pass
// a closure over Provider.removeLegacySecretsDirViaHelper). A nil removeStuck,
// or a removeStuck that itself errors, falls back to the original WARN +
// failed accounting. On removeStuck success, removeAll is retried once to
// finish removing the (now-empty) directory skeleton.
func sweepLegacySecretsBase(base string, protected map[string]bool, logger *slog.Logger, removeAll func(string) error, removeStuck func(string) error) (removed, skipped, failed int) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("legacy secrets sweep: cannot read base dir", "path", base, "error", err)
		}
		return 0, 0, 0
	}
	for _, e := range entries {
		name := e.Name()
		if protected[name] {
			skipped++
			continue
		}
		dir := filepath.Join(base, name)
		removeErr := removeAll(dir)
		if removeErr == nil {
			removed++
			continue
		}
		if removeStuck == nil {
			failed++
			logger.Warn("legacy secrets sweep: removal failed; cleartext credentials may remain on disk",
				"path", dir, "error", removeErr)
			continue
		}
		// Likely cause: a dormant per-agent subdirectory created by the
		// container as UID 1001, mode 0700 (#1069) — this process can't
		// open it to remove its contents. Recover via a UID-1001 helper
		// container, then retry the plain removal to finish the now-empty
		// directory skeleton (that only needs write permission on the
		// parent, which this process already has).
		if helperErr := removeStuck(dir); helperErr != nil {
			failed++
			logger.Warn("legacy secrets sweep: removal failed even via UID-1001 helper container; cleartext credentials may remain on disk",
				"path", dir, "error", removeErr, "helper_error", helperErr)
			continue
		}
		if retryErr := removeAll(dir); retryErr != nil {
			failed++
			logger.Warn("legacy secrets sweep: removal still failed after UID-1001 helper container ran; cleartext credentials may remain on disk",
				"path", dir, "error", retryErr)
			continue
		}
		removed++
		logger.Info("legacy secrets sweep: removed dormant dir via UID-1001 helper container", "path", dir)
	}
	// Drop the base dir itself when nothing is left in it. Best-effort:
	// os.Remove refuses non-empty dirs, which is exactly what we want when
	// protected entries remain.
	_ = os.Remove(base)
	return removed, skipped, failed
}

// removeLegacySecretsDirViaHelper drains a dormant per-agent secrets dir that
// removeAll could not touch because its contents are owned by the in-container
// UID 1001 (mode 0700) rather than the crewship server's own UID, which runs
// without CAP_DAC_OVERRIDE and cannot descend into it (#1069).
//
// Mirrors the mechanism internal/orchestrator/exec_sidecar.go's
// writeCredentialFiles uses to WRITE those same files: a short-lived
// container running as UID 1001 — the dirs' actual owner — so no root or
// capability gymnastics are needed. The helper only empties file contents
// (`find -delete`, depth-first, so it descends into any nested subdirectory);
// it does not need to remove dirPath's own directory entries top-down, since
// that requires write permission on THEIR PARENT (dirPath itself), which the
// helper doesn't have (dirPath is bind-mounted from a host path owned by the
// crewship server, not UID 1001) — that last step is what the caller's
// removeAll retry (after this returns) finishes off using the permission it
// already has. A non-zero helper exit is therefore expected in the common
// case and is not treated as failure here; only a Docker-level error (create,
// start, or wait failing outright) is.
func (p *Provider) removeLegacySecretsDirViaHelper(ctx context.Context, dirPath string) error {
	image := p.cfg.RuntimeImage
	if image == "" {
		return fmt.Errorf("no runtime image configured; cannot start UID-1001 removal helper")
	}

	helperCfg := &container.Config{
		Image:      image,
		User:       "1001:1001",
		Entrypoint: []string{"sh", "-c", "find /legacy -mindepth 1 -depth -delete"},
	}
	helperHost := &container.HostConfig{
		NetworkMode: "none",
		CapDrop:     []string{"ALL"},
		Mounts: []mount.Mount{
			{Type: mount.TypeBind, Source: dirPath, Target: "/legacy"},
		},
	}

	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     helperCfg,
		HostConfig: helperHost,
	})
	if err != nil {
		return fmt.Errorf("create removal helper: %w", err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
	}()

	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start removal helper: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, legacySecretsHelperTimeout)
	defer cancel()
	wait := p.client.ContainerWait(waitCtx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case _, ok := <-wait.Result:
		if !ok {
			return fmt.Errorf("wait for removal helper: wait channel closed before a status was delivered")
		}
		// Exit code intentionally not checked — see doc comment above.
		return nil
	case werr := <-wait.Error:
		return fmt.Errorf("wait for removal helper: %w", werr)
	case <-waitCtx.Done():
		return fmt.Errorf("wait for removal helper: %w", waitCtx.Err())
	}
}

// sweepLegacySecretsDirs runs the one-time startup scrub. Called from New;
// requires the docker client (to know which legacy dirs are still mounted).
func (p *Provider) sweepLegacySecretsDirs(ctx context.Context) {
	if p.cfg.OutputBasePath == "" {
		return
	}
	base := filepath.Join(p.cfg.OutputBasePath, "secrets")
	if _, err := os.Stat(base); err != nil {
		return // nothing to scrub (the common steady state)
	}
	listResult, err := p.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		// Without the mount inventory we cannot tell live dirs from dead
		// ones — deleting blindly could break running legacy agents, so skip.
		p.logger.Warn("legacy secrets sweep skipped: cannot list containers", "error", err)
		return
	}
	protected := protectedLegacySecretsDirs(listResult.Items, base)
	removed, skipped, failed := sweepLegacySecretsBase(base, protected, p.logger, os.RemoveAll,
		func(dir string) error { return p.removeLegacySecretsDirViaHelper(ctx, dir) })
	if removed+skipped+failed > 0 {
		p.logger.Info("legacy host-side secrets sweep complete",
			"removed", removed, "still_mounted_skipped", skipped, "failed", failed)
	}
}
