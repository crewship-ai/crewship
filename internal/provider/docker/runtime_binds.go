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
//     crews on a default Colima with no configuration at all.
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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// stagedRuntimeDirName is the subdirectory of OutputBasePath that holds the
// bind-mountable copies. Dot-prefixed so it can never collide with a crew id
// (cuid2, never leading-dot) and never with the reserved "crews"/"workspaces"/
// "secrets" subtrees prepareCrewDirs owns. It is NOT mounted into any
// container — only the two files inside it are, read-only, at their existing
// /usr/local/bin targets — so it stays out of reach of every agent.
const stagedRuntimeDirName = ".runtime"

// stageRuntimeArtifacts returns cfg with SidecarBinaryPath and EntrypointPath
// repointed at copies under OutputBasePath/.runtime, so the mandatory binds
// live in the same host subtree as the crew data dirs.
//
// Unconditional rather than "only when the daemon looks VM-backed". There is no
// API that enumerates a VM's share set, so any conditional version is a guess
// about which runtimes need it — which is exactly how #1706 stayed invisible to
// everyone developing on OrbStack. One path, exercised on every runtime, beats
// a fast path that only three of them take.
//
// Best-effort: any failure leaves cfg untouched and logs why, because a copy
// error must degrade to today's behaviour (which works on a native daemon)
// rather than take the provider down.
func stageRuntimeArtifacts(cfg Config, logger *slog.Logger) Config {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.OutputBasePath == "" {
		// No data dir to stage into — a hand-built provider (tests) or an
		// embedding that never persists crew state. Leave the paths alone.
		return cfg
	}
	dir := filepath.Join(cfg.OutputBasePath, stagedRuntimeDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warn("could not create the runtime staging dir; crew containers will bind the sidecar and entrypoint from their install location, which a VM-backed runtime may not be able to see (#1706)",
			"dir", dir, "error", err)
		return cfg
	}

	if staged, err := stageArtifact(cfg.SidecarBinaryPath, filepath.Join(dir, "crewship-sidecar")); err != nil {
		logger.Warn("could not stage crewship-sidecar next to the crew data dirs; binding it from its install path instead (#1706)",
			"source", cfg.SidecarBinaryPath, "error", err)
	} else if staged != "" {
		logger.Info("staged crewship-sidecar for bind-mounting",
			"install_path", cfg.SidecarBinaryPath, "bind_source", staged)
		cfg.SidecarBinaryPath = staged
	}

	if staged, err := stageArtifact(cfg.EntrypointPath, filepath.Join(dir, "entrypoint.sh")); err != nil {
		logger.Warn("could not stage entrypoint.sh next to the crew data dirs; binding it from its install path instead (#1706)",
			"source", cfg.EntrypointPath, "error", err)
	} else if staged != "" {
		cfg.EntrypointPath = staged
	}
	return cfg
}

// stageArtifact copies src to dst and returns dst. An empty src is not an
// error — the caller's config simply has no such artifact — and returns "".
//
// The copy is atomic (temp file + rename) so two servers sharing a data dir
// cannot bind a half-written sidecar, and it PRESERVES the source's mtime:
// assertSidecarFreshAtStartup compares the sidecar's mtime against the server
// binary's to catch a deploy that updated one and not the other, and a copy
// that stamped "now" would silence that check on every single boot.
func stageArtifact(src, dst string) (string, error) {
	if src == "" {
		return "", nil
	}
	if same, err := sameFile(src, dst); err == nil && same {
		// Already the staged copy (a re-entrant call, or an operator who
		// pinned CREWSHIP_SIDECAR_PATH at it). Copying a file onto itself
		// would truncate it.
		return dst, nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	in, err := os.Open(src) // #nosec G304 — src is an operator-configured artifact path
	if err != nil {
		return "", err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".stage-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeded

	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", err
	}
	if err := os.Chtimes(tmpName, info.ModTime(), info.ModTime()); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// sameFile reports whether two paths are the same file on disk.
func sameFile(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
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
