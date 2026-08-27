package docker

// Host paths a VM-backed daemon can actually see (#1706).
//
// Every crew container bind-mounts host paths, and a bind source is a string
// the *daemon* resolves, not this process. On a native Linux daemon those are
// the same filesystem and the distinction never surfaces. On a VM-backed
// runtime — Colima, Rancher Desktop, Docker Desktop, podman machine, Apple
// containers — the daemon lives inside a VM that shares only a configured set
// of host directories, and a bind source outside that set does not exist as
// far as the daemon is concerned.
//
// That breaks the most common install outright. crewship-sidecar and
// entrypoint.sh are MANDATORY binds (buildMounts errors without them), and
// internal/config resolves them next to the crewship executable: Homebrew puts
// that at /opt/homebrew/..., install.sh at /usr/local/bin. Colima shares only
// $HOME by default, so neither exists in its VM and every crew create fails
// with `bind source path does not exist`. The operator is told a file is
// missing when it is plainly there.
//
// Two things happen here:
//
//  1. stageRuntimeArtifacts copies the sidecar and entrypoint under the crew
//     data dir, and the provider binds the copies. The data dir already has to
//     be visible to the daemon — /workspace, /output and /crew are bind-mounted
//     out of it, so nothing works if it is not — which collapses two separate
//     "this path must be shared" requirements into the one that was already
//     load-bearing. On the default install ($HOME/.crewship/output) that is
//     inside Colima's default share and a Homebrew-installed crewship starts
//     crews on a default Colima with no configuration at all. The copy itself
//     lives in internal/provider/runtimestage because the apple provider needs
//     exactly the same one (#1724) — including the mtime preservation that
//     sidecar_freshness.go depends on, which is precisely the detail a second
//     hand-written copy would drop.
//
//  2. explainBindFailure turns the daemon's `bind source path does not exist`
//     into a message that says what is actually wrong, for the cases staging
//     cannot fix — a data dir outside the share set, a remote daemon, a
//     hand-pinned CREWSHIP_SIDECAR_PATH. The discriminator is exact rather
//     than a guess: the path exists on THIS host and the daemon says it does
//     not, which is only ever true when the daemon is looking at a different
//     filesystem.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// stageRuntimeArtifacts returns cfg with SidecarBinaryPath and EntrypointPath
// repointed at copies under OutputBasePath/.runtime, so the mandatory binds
// live in the same host subtree as the crew data dirs.
//
// A thin mapping of this package's Config onto runtimestage.Artifacts, which
// holds the staging rules — unconditional, best-effort, atomic and
// mtime-preserving — and the reasoning for each of them.
func stageRuntimeArtifacts(cfg Config, logger *slog.Logger) Config {
	cfg.SidecarBinaryPath, cfg.EntrypointPath = runtimestage.Artifacts(
		cfg.OutputBasePath, cfg.SidecarBinaryPath, cfg.EntrypointPath, logger)
	return cfg
}

// bindSourceMissingPatterns extract the host path out of a daemon's
// "the bind source is not there" rejection. Two spellings, because the two
// engine families word it differently and both are reachable here:
//
//	moby:   invalid mount config for type "bind": bind source path does not exist: /opt/homebrew/...
//	podman: statfs /opt/homebrew/...: no such file or directory
//
// Verified against Colima 29.5.2, OrbStack 29.4.0 and Rancher Desktop 29.5.3 —
// all three reject the mount BEFORE resolving the image, which is what lets
// preflightMandatoryBinds probe with a sentinel image that does not exist.
//
// The captures run to the end of the message rather than to the next space:
// host paths contain spaces (`/Volumes/SSD 990 PRO/...` is where this was
// caught, live on Colima), and a `\S+` capture truncates them to a prefix that
// does not exist on this host either — which makes explainBindFailure decide
// the file is genuinely missing and pass the useless error straight through.
var bindSourceMissingPatterns = []*regexp.Regexp{
	regexp.MustCompile(`bind source path does not exist:\s*(.+)$`),
	regexp.MustCompile(`statfs\s+(.+): no such file or directory`),
}

// unreachableBindSourcePath returns the host path a daemon said was missing, or
// "" when err is not that kind of failure.
func unreachableBindSourcePath(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, re := range bindSourceMissingPatterns {
		if m := re.FindStringSubmatch(msg); m != nil {
			return strings.TrimRight(strings.TrimSpace(m[1]), `.,"`)
		}
	}
	return ""
}

// explainBindFailure rewrites a bind-source-missing error into one that names
// the real fault, or returns err unchanged when it is something else.
//
// The discriminator is not a heuristic about which runtimes are VM-backed: the
// daemon said the path does not exist and this process can stat it, so the
// daemon is demonstrably looking at a different filesystem. When the path is
// genuinely absent here too, the original error is already correct and is left
// alone — turning a missing file into a lecture about VM shares would send the
// operator to the wrong place.
func (p *Provider) explainBindFailure(err error) error {
	path := unreachableBindSourcePath(err)
	if path == "" {
		return err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return err
	}
	return fmt.Errorf(
		"%w — the %s daemon cannot see the host path %s. It exists on this machine, so this is not a missing file: %s runs the daemon inside a VM that only shares the host directories it was configured with, and this path is not one of them. Fix it either way:\n"+
			"  • share the path with the VM: %s\n"+
			"  • or move what is being bound under a directory the VM already shares — Crewship stages crewship-sidecar and entrypoint.sh under its data dir (%s) for exactly this reason, so pointing --data-dir (or CREWSHIP_DATA_DIR) at a shared location fixes every crew bind at once",
		err, p.detected.Runtime, path, p.detected.Runtime,
		vmShareRemedy(p.detected.Runtime, path),
		emptyOr(p.cfg.OutputBasePath, "<unset>"),
	)
}

// emptyOr returns fallback when s is empty.
func emptyOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// vmShareRemedy is the runtime-specific command or setting that adds a host
// directory to the VM's share set. The directory rather than the file: every
// one of these shares directories, and telling someone to share a single file
// produces a second, more confusing failure.
func vmShareRemedy(runtime, path string) string {
	dir := filepath.Dir(path)
	switch runtime {
	case "colima":
		return fmt.Sprintf("`colima stop && colima start --mount %s:w` (or add it to the `mounts:` list in ~/.colima/default/colima.yaml and restart)", dir)
	case "rancher":
		return fmt.Sprintf("Rancher Desktop → Preferences → Virtual Machine → Volumes, add %s, then restart the VM", dir)
	case "podman":
		return fmt.Sprintf("`podman machine stop && podman machine set --volume %s && podman machine start` (a new machine needs `podman machine init --volume %s`)", dir, dir)
	case "docker":
		return fmt.Sprintf("Docker Desktop → Settings → Resources → File sharing, add %s, then Apply & restart", dir)
	case "orbstack":
		// OrbStack shares the whole filesystem, so landing here means the path
		// is unreadable rather than unshared — say so instead of inventing a
		// share setting it does not have.
		return fmt.Sprintf("OrbStack shares the entire host filesystem, so an unreachable %s points at a permission problem or a path on an unmounted volume rather than a share setting — check that the crewship process can read it", dir)
	default:
		return fmt.Sprintf("add %s to the host directories your container runtime's VM shares, then restart it", dir)
	}
}

// preflightSentinelImage is a deliberately unresolvable image reference. Every
// engine measured validates the mount config BEFORE it looks the image up, so a
// create with this image and the real binds answers "can you see these paths?"
// without needing an image pulled, a container started, or anything cleaned up:
// the create always fails, and only the SHAPE of the failure is read.
const preflightSentinelImage = "crewship-bind-preflight:does-not-exist"

// preflightMandatoryBinds checks at boot that the daemon can see the sidecar
// and entrypoint bind sources, instead of leaving it to blow up on a user's
// first prompt (#1706).
//
// It logs rather than returning an error, and that is a call site fact rather
// than timidity: with `container.provider: auto` (the default),
// selectAutoContainerProvider treats a New() error as "this provider is not
// available" and silently moves on to the next candidate, logging the reason at
// DEBUG. A hard failure here would therefore make the most actionable message
// in the codebase the least visible one. The same explanation is attached to
// the create error too, where it reaches the user's provisioning failure.
func (p *Provider) preflightMandatoryBinds(ctx context.Context) {
	mounts, err := p.buildMounts("preflight", "", "", "", "")
	if err != nil {
		return // no sidecar/entrypoint configured; validateSidecarPaths already said so
	}
	// Keep only the read-only sidecar/entrypoint binds: the crew data dirs are
	// per-crew and do not exist yet at boot.
	var binds []mount.Mount
	for _, m := range mounts {
		if m.Type == mount.TypeBind {
			binds = append(binds, m)
		}
	}
	if len(binds) == 0 {
		return
	}
	_, createErr := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: preflightSentinelImage},
		HostConfig: &container.HostConfig{Mounts: binds},
	})
	// The only outcome that means anything is a bind-source rejection. An
	// image-not-found error (the expected one) says the mounts validated;
	// anything else is a daemon problem this probe has no opinion about.
	if unreachableBindSourcePath(createErr) == "" {
		return
	}
	p.logger.Error("no crew can start on this container runtime: a mandatory bind source is unreachable from the daemon (#1706)",
		"runtime", p.detected.Runtime,
		"socket", p.detected.Socket,
		"error", p.explainBindFailure(createErr).Error(),
	)
}
