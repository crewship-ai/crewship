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

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

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
func sweepLegacySecretsBase(base string, protected map[string]bool, logger *slog.Logger) (removed, skipped, failed int) {
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
		if err := os.RemoveAll(filepath.Join(base, name)); err != nil {
			failed++
			logger.Warn("legacy secrets sweep: removal failed; cleartext credentials may remain on disk",
				"path", filepath.Join(base, name), "error", err)
			continue
		}
		removed++
	}
	// Drop the base dir itself when nothing is left in it. Best-effort:
	// os.Remove refuses non-empty dirs, which is exactly what we want when
	// protected entries remain.
	_ = os.Remove(base)
	return removed, skipped, failed
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
	containers, err := p.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		// Without the mount inventory we cannot tell live dirs from dead
		// ones — deleting blindly could break running legacy agents, so skip.
		p.logger.Warn("legacy secrets sweep skipped: cannot list containers", "error", err)
		return
	}
	protected := protectedLegacySecretsDirs(containers, base)
	removed, skipped, failed := sweepLegacySecretsBase(base, protected, p.logger)
	if removed+skipped+failed > 0 {
		p.logger.Info("legacy host-side secrets sweep complete",
			"removed", removed, "still_mounted_skipped", skipped, "failed", failed)
	}
}
