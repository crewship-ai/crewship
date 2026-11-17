package docker

// Runtime artifacts are staged in the server's local storage subtree. Docker
// resolves bind sources in the daemon namespace: a VM or a sibling server
// container can see different paths. daemon_paths.go translates only Docker
// request paths; staging, hashes and local file checks keep local paths.
// Native/VM installations sharing the same paths retain staging behavior from
// #1706. Runtime staging is shared with the Apple provider via runtimestage.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
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
	sidecarSource := cfg.SidecarBinaryPath
	cfg.SidecarBinaryPath, cfg.EntrypointPath = runtimestage.Artifacts(
		cfg.OutputBasePath, cfg.SidecarBinaryPath, cfg.EntrypointPath, logger)
	if strings.TrimSpace(os.Getenv("CREWSHIP_MANAGED_LAUNCH_CREWS")) != "" {
		if immutable, err := stageManagedLauncher(sidecarSource, cfg.OutputBasePath); err == nil {
			cfg.SidecarBinaryPath = immutable
		} else {
			logger.Warn("managed launcher staging failed; pilot admission will refuse unqualified binds", "error", err)
		}
	}
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
	localPath := path
	if p.pathMap != nil {
		mapped, mapErr := p.localBindSource(path)
		if mapErr == nil {
			localPath = mapped
		}
	}
	if _, statErr := os.Stat(localPath); statErr != nil {
		return err
	}
	return fmt.Errorf("%w — the %s daemon cannot see %s, which exists on this machine. The server and daemon use different filesystems or mount namespaces. For a containerized server configure CREWSHIP_DOCKER_SERVER_CONTAINER or CREWSHIP_DOCKER_DAEMON_DATA_PATH; for a VM-backed runtime check shared directories: %s (storage: %s)", err, p.detected.Runtime, path+" (local: "+localPath+")", vmShareRemedy(p.detected.Runtime, localPath), emptyOr(p.cfg.OutputBasePath, "<unset>"))
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

// Probe errors are classified conservatively for unqualified runtimes. Only
// local configuration/mapping errors and recognized daemon bind-source rejection
// block runtime admission. Inconclusive API replies are logged, never promoted
// to a new unavailable state. Only verified success is cached for 30 seconds.
const runtimeProbeTTL = 30 * time.Second

type mandatoryProbeState struct {
	err        error
	verifiedAt time.Time
}

func (p *Provider) runtimeAvailable(ctx context.Context) error {
	if p.runtimeErr != nil || p.runtimeProbeGate == nil {
		return p.runtimeErr
	}
	select {
	case p.runtimeProbeGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.runtimeProbeGate }()
	previous := p.runtimeProbeState.Load()
	if previous != nil && previous.err == nil && !previous.verifiedAt.IsZero() && time.Since(previous.verifiedAt) < runtimeProbeTTL {
		return nil
	}
	verified, err := p.probeMandatoryBinds(ctx)
	// An inconclusive retry does not disprove a known prior bind/config failure.
	if !verified && err == nil && previous != nil {
		err = previous.err
	}
	state := &mandatoryProbeState{err: err}
	if verified {
		state.verifiedAt = time.Now()
	}
	p.runtimeProbeState.Store(state)
	return err
}

// Keep a slow or queued probe from consuming the readiness handler's DB/state
// budget or delaying warm crew reuse. The prior recognized failure remains
// blocking; an unknown response or probe wait alone is not a runtime verdict.
func (p *Provider) runtimeAvailableForRequest(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.runtimeErr != nil || p.runtimeProbeGate == nil {
		return p.runtimeErr
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	err := p.runtimeAvailable(probeCtx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil && probeCtx.Err() != nil {
		if previous := p.runtimeProbeState.Load(); previous != nil && previous.err != nil {
			return previous.err
		}
		p.logger.Warn("mandatory bind probe inconclusive; retaining runtime availability", "error", err)
		return nil
	}
	return err
}

func (p *Provider) preflightMandatoryBinds(ctx context.Context) {
	if err := p.runtimeAvailable(ctx); err != nil {
		p.logger.Error("no crew can start on this container runtime: mandatory mount preflight failed", "runtime", p.detected.Runtime, "socket", p.detected.Socket, "error", err)
	}
}

func (p *Provider) probeMandatoryBinds(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for _, source := range []string{p.cfg.SidecarBinaryPath, p.cfg.EntrypointPath} {
		info, err := os.Stat(source)
		if err != nil {
			return false, fmt.Errorf("mandatory runtime file %s: %w", source, err)
		}
		if !runtimeFileModeAllowed(info.Mode(), goruntime.GOOS) {
			return false, fmt.Errorf("mandatory runtime file %s must be a regular executable", source)
		}
	}
	mounts, err := p.buildMounts("preflight", "", "", "", "")
	if err != nil {
		return false, err
	}
	var binds []mount.Mount
	for _, m := range mounts {
		if m.Type == mount.TypeBind {
			binds = append(binds, m)
		}
	}
	if len(binds) == 0 {
		return true, nil
	}
	created, createErr := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: preflightSentinelImage}, HostConfig: &container.HostConfig{Mounts: binds},
	})
	if createErr == nil {
		_, createErr = p.client.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{})
		if createErr == nil || cerrdefs.IsNotFound(createErr) {
			return true, nil
		}
	}
	if cerrdefs.IsNotFound(createErr) && strings.HasSuffix(strings.TrimSpace(createErr.Error()), "No such image: "+preflightSentinelImage) {
		return true, nil
	}
	if unreachableBindSourcePath(createErr) != "" {
		return false, p.explainBindFailure(createErr)
	}
	p.logger.Warn("mandatory bind probe inconclusive; retaining runtime availability", "error", createErr)
	return false, nil
}

// A digest-addressed bind source is never replaced. Docker archive reads mount
// a temporary filesystem view and cannot prove the inode in a running bind.
// Retain old generations for existing containers; garbage collection is separate.
func stageManagedLauncher(source, output string) (string, error) {
	if err := managedLauncherServerUIDError(os.Geteuid()); err != nil {
		return "", err
	}
	return stageManagedLauncherWithHostTrust(source, output, safeLauncherOwner, safeLauncherDirectory)
}

// Only unit fixtures replace host ownership trust; production staging always
// enters through the UID guard and the real owner/directory validators above.
func stageManagedLauncherWithHostTrust(source, output string, owner, directory func(os.FileInfo) bool) (string, error) {
	denied := errors.New("managed launch: safe immutable launcher staging required")
	if source == "" || output == "" {
		return "", denied
	}
	dir := filepath.Join(output, runtimestage.DirName)
	info, err := os.Lstat(dir)
	if err != nil || !directory(info) {
		return "", denied
	}
	for parent := filepath.Dir(dir); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !directory(info) {
			return "", fmt.Errorf("%w: unsafe ancestor %s", denied, parent)
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	f, err := os.Open(source)
	if err != nil {
		return "", err
	}
	raw, err := io.ReadAll(io.LimitReader(f, managedlaunch.MaxArtifactBytes+1))
	f.Close()
	if err != nil || len(raw) == 0 || len(raw) > managedlaunch.MaxArtifactBytes {
		return "", denied
	}
	sum := sha256.Sum256(raw)
	dest := filepath.Join(dir, runtimestage.SidecarFileName+"-"+hex.EncodeToString(sum[:]))
	tmp, err := os.CreateTemp(dir, ".managed-launcher-")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0555); err != nil {
		return "", err
	}
	if original, err := os.Stat(source); err == nil {
		if err := os.Chtimes(tmp.Name(), original.ModTime(), original.ModTime()); err != nil {
			return "", err
		}
	}
	// Link publishes atomically and fails if the destination already exists.
	if err := os.Link(tmp.Name(), dest); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err = os.Lstat(dest)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 || !owner(info) {
		return "", denied
	}
	f, err = os.Open(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	length, err := io.Copy(hash, io.LimitReader(f, managedlaunch.MaxArtifactBytes+1))
	if err != nil || length != int64(len(raw)) || hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(sum[:]) {
		return "", denied
	}
	return dest, nil
}

func managedLauncherServerUIDError(uid int) error {
	if uid == 1001 || uid == 1002 {
		return fmt.Errorf("managed launch: server UID %d collides with agent/sidecar UID; use a host-native server under a different UID (official server image unsupported for this pilot)", uid)
	}
	return nil
}

// Windows reports regular files as 0444/0666 regardless of executable content.
// Preserve its Docker Desktop path while checking POSIX execute bits elsewhere.
func runtimeFileModeAllowed(mode os.FileMode, hostOS string) bool {
	return mode.IsRegular() && (hostOS == "windows" || mode.Perm()&0111 != 0)
}
