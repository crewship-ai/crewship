package docker

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// daemonPathMap is immutable after startup. Only request paths are translated;
// cfg paths remain local for staging, hashing, ownership and cleanup.
type daemonPathMap struct{ local, daemon string }

func cleanAbsolutePath(p string) bool { return filepath.IsAbs(p) && filepath.Clean(p) == p }

func (p *Provider) resolveDaemonPaths(ctx context.Context) (*daemonPathMap, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	local := p.cfg.OutputBasePath
	remote := p.cfg.DaemonDataPath
	if remote != "" && p.cfg.ServerContainer != "" {
		return nil, fmt.Errorf("configure either daemon data path or server container, not both")
	}
	if remote == "" && p.cfg.ServerContainer == "" {
		return nil, nil
	} // native / VM shared-path installation
	if !cleanAbsolutePath(local) {
		return nil, fmt.Errorf("Docker path mapping requires a clean absolute storage base path")
	}
	if p.cfg.ServerContainer != "" {
		inspected, err := p.client.ContainerInspect(ctx, p.cfg.ServerContainer, client.ContainerInspectOptions{})
		if err != nil {
			return nil, fmt.Errorf("inspect configured Docker server container: %w", err)
		}
		hostname, hostErr := os.Hostname()
		if hostErr != nil {
			return nil, fmt.Errorf("read server hostname: %w", hostErr)
		}
		if inspected.Container.Config == nil || inspected.Container.Config.Hostname != hostname {
			return nil, fmt.Errorf("configured Docker server container does not match this server hostname")
		}
		var selected *container.MountPoint
		for i := range inspected.Container.Mounts {
			m := &inspected.Container.Mounts[i]
			if !cleanAbsolutePath(m.Destination) || !pathContains(m.Destination, local) {
				continue
			}
			if selected == nil || len(m.Destination) > len(selected.Destination) {
				selected = m
			}
		}
		if selected == nil {
			return nil, fmt.Errorf("configured server container has no storage mount covering %s", local)
		}
		if !selected.RW || (selected.Type != mount.TypeBind && selected.Type != mount.TypeVolume) || (selected.Type == mount.TypeVolume && selected.Driver != "local") || !cleanAbsolutePath(selected.Source) {
			return nil, fmt.Errorf("server storage must be a writable bind or local volume with an absolute daemon source")
		}
		best := selected.Destination
		rel, _ := filepath.Rel(best, local)
		remote = filepath.Join(selected.Source, rel)

		for _, m := range inspected.Container.Mounts {
			if m.Destination != best && pathContains(local, m.Destination) {
				return nil, fmt.Errorf("nested storage mounts require a single shared storage root")
			}
		}
	}
	if !cleanAbsolutePath(remote) || remote == string(filepath.Separator) || local == string(filepath.Separator) {
		return nil, fmt.Errorf("Docker storage mapping requires non-root clean absolute paths")
	}
	return &daemonPathMap{local: local, daemon: remote}, nil
}

func (m *daemonPathMap) translate(source string) (string, error) {
	if source == "" {
		return source, nil
	} // canonical preflight data mounts are omitted
	if !cleanAbsolutePath(source) || !pathContains(m.local, source) {
		return "", fmt.Errorf("Docker bind source %q is outside configured local storage root %q", source, m.local)
	}
	root, err := filepath.EvalSymlinks(m.local)
	if err != nil {
		return "", fmt.Errorf("resolve local storage root: %w", err)
	}
	if root != m.local {
		return "", fmt.Errorf("Docker mapping storage root must be canonical; use %s", root)
	}
	resolved, err := prospectiveMappingPath(source)
	if err != nil {
		return "", fmt.Errorf("resolve Docker bind source: %w", err)
	}
	if !pathContains(root, resolved) {
		return "", fmt.Errorf("Docker bind source %q escapes storage through a symlink", source)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("Docker bind source escapes storage")
	}
	return filepath.Join(m.daemon, rel), nil
}

// Resolve existing references before retaining an ordinary missing suffix.
// An existing but unresolved reference cannot establish a shared path identity.
func prospectiveMappingPath(source string) (string, error) {
	current := source
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || current == filepath.Dir(current) {
			return "", err
		}
		if _, statErr := os.Lstat(current); statErr == nil {
			return "", fmt.Errorf("cannot map unresolved filesystem reference %s", current)
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
		tail = append(tail, filepath.Base(current))
		current = filepath.Dir(current)
	}
}

// daemonMounts owns the local-to-daemon boundary, including local driver device
// paths. Named volumes and container targets are never rewritten.
func (p *Provider) daemonMounts(mounts []mount.Mount) ([]mount.Mount, error) {
	if p.runtimeErr != nil {
		return nil, p.runtimeErr
	}
	if p.pathMap == nil {
		return mounts, nil
	}
	out := append([]mount.Mount(nil), mounts...)
	for i := range out {
		m := &out[i]
		var err error
		if m.Type == mount.TypeBind {
			m.Source, err = p.pathMap.translate(m.Source)
		}
		if m.VolumeOptions != nil && m.VolumeOptions.DriverConfig != nil && m.VolumeOptions.DriverConfig.Options["device"] != "" {
			options := *m.VolumeOptions
			driver := *options.DriverConfig
			driver.Options = make(map[string]string, len(options.DriverConfig.Options))
			for k, v := range options.DriverConfig.Options {
				driver.Options[k] = v
			}
			driver.Options["device"], err = p.pathMap.translate(driver.Options["device"])
			options.DriverConfig = &driver
			m.VolumeOptions = &options
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *Provider) daemonPathForLocal(local string) (string, error) {
	if p.pathMap != nil {
		return p.pathMap.translate(local)
	}
	if p.cfg.DaemonDataPath != "" || p.cfg.ServerContainer != "" {
		return "", fmt.Errorf("configured Docker storage mapping is unavailable")
	}
	return local, nil
}

// matchesDaemonPath compares an inspected daemon path with the sole path
// this provider would supply for a trusted local bind.
func (p *Provider) matchesDaemonPath(inspected, local string) bool {
	expected, err := p.daemonPathForLocal(local)
	return err == nil && inspected == expected
}

// localBindSource authenticates an inspect path in the server filesystem. Paths
// outside the configured mapping cannot be authenticated from this process.
func (p *Provider) localBindSource(source string) (string, error) {
	if p.pathMap == nil {
		if _, err := p.daemonPathForLocal(source); err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(source)
	}
	if !cleanAbsolutePath(source) || !pathContains(p.pathMap.daemon, source) {
		return "", fmt.Errorf("inspected bind lies outside configured daemon storage root")
	}
	rel, _ := filepath.Rel(p.pathMap.daemon, source)
	local := filepath.Join(p.pathMap.local, rel)
	expected, err := p.pathMap.translate(local)
	if err != nil || expected != source {
		return "", fmt.Errorf("inspected bind cannot be authenticated through storage mapping")
	}
	return filepath.EvalSymlinks(local)
}

// RuntimeReady retains a failed mandatory mount check without removing the
// provider (which would silently downgrade auto mode to a different runtime).
func (p *Provider) RuntimeReady(ctx context.Context) error {
	if err := p.runtimeAvailableForRequest(ctx); err != nil {
		return err
	}
	_, err := p.client.Ping(ctx, client.PingOptions{})
	return err
}

// Mapped runtimes share a daemon with the server's private volumes. A
// feature's arbitrary volume name carries no ownership proof on that daemon.
func (p *Provider) checkMappedFeatureVolumes(team provider.CrewConfig) error {
	if p.pathMap != nil {
		for _, m := range team.ExtraMounts {
			if strings.EqualFold(m.Type, "volume") {
				return fmt.Errorf("feature-declared named volumes are unavailable with mapped Docker storage")
			}
		}
	}
	return nil
}

var errMappedRuntimeMount = errors.New("mapped runtime mount rejected")

// Check the mounts returned by Docker. A cached or restarted runtime may
// have been created under older rules.
func (p *Provider) checkMappedRuntimeMounts(ctx context.Context, mounts []container.MountPoint, team provider.CrewConfig) error {
	if p.pathMap == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	bindTargets := map[string]string{
		"/workspace": filepath.Join(p.pathMap.local, "workspaces", team.ID),
		"/output":    filepath.Join(p.pathMap.local, team.ID),
		"/crew":      filepath.Join(p.pathMap.local, "crews", team.ID),
	}
	helperBinds := map[string]string{
		"/usr/local/bin/crewship-sidecar": p.cfg.SidecarBinaryPath,
		"/usr/local/bin/entrypoint.sh":    p.cfg.EntrypointPath,
	}
	for _, m := range mounts {
		switch m.Type {
		case mount.TypeBind:
			if !m.RW && m.Destination == agentCLIExecutable && p.matchesMappedAgentCLIGeneration(m.Source) {
				continue
			}
			if local := helperBinds[m.Destination]; local == "" || m.RW || !p.matchesDaemonPath(m.Source, local) {
				return fmt.Errorf("%w: unexpected or writable helper bind", errMappedRuntimeMount)
			}
			continue
		case mount.TypeTmpfs:
			if m.Source != "" || (m.Destination != "/tmp" && m.Destination != "/secrets") {
				return fmt.Errorf("%w: unexpected temporary filesystem", errMappedRuntimeMount)
			}
			continue
		case mount.TypeVolume:
		default:
			return fmt.Errorf("%w: unsupported daemon mount type", errMappedRuntimeMount)
		}
		managedPath := bindTargets[m.Destination]
		switch m.Destination {
		case "/home/agent":
			if team.Slug == "" || m.Name != p.homeVolumeName(team.ID, team.Slug) {
				return fmt.Errorf("%w: has an unexpected home volume", errMappedRuntimeMount)
			}
		case "/opt/crew-tools":
			if team.Slug == "" || m.Name != p.toolsVolumeName(team.ID, team.Slug) {
				return fmt.Errorf("%w: has an unexpected tools volume", errMappedRuntimeMount)
			}
		default:
			if managedPath == "" {
				return fmt.Errorf("%w: contains an unowned volume", errMappedRuntimeMount)
			}
		}
		result, err := p.client.VolumeInspect(ctx, m.Name, client.VolumeInspectOptions{})
		if err != nil {
			return fmt.Errorf("validate mapped runtime volume: %w", err)
		}
		v := result.Volume
		if v.Name != m.Name || v.Driver != "local" || !cleanAbsolutePath(v.Mountpoint) || v.Mountpoint != m.Source {
			return fmt.Errorf("%w: volume cannot be authenticated", errMappedRuntimeMount)
		}
		if managedPath != "" {
			device, err := p.daemonPathForLocal(managedPath)
			if err != nil {
				return err
			}
			if v.Labels[crewCrewIDLabel] != team.ID || v.Labels[crewKindLabel] != noexecBindVolumeKind ||
				v.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || v.Options["type"] != "none" ||
				v.Options["o"] != noexecBindMountOpts || v.Options["device"] != device {
				return fmt.Errorf("%w: data volume does not match the crew's storage", errMappedRuntimeMount)
			}
		} else if err := p.checkMappedManagedVolume(v, m.Name, team.ID); err != nil {
			return err
		}
		if p.mappedVolumeOverlapsStorage(v.Mountpoint) {
			return fmt.Errorf("%w: volume overlaps server storage", errMappedRuntimeMount)
		}
	}
	return nil
}

// Existing runs can keep a previous immutable generation. The runtime
// reconciler decides when to replace it through the idle gate.
func (p *Provider) matchesMappedAgentCLIGeneration(source string) bool {
	current := p.cfg.agentCLIArtifact
	if current == nil {
		return false
	}
	if p.matchesDaemonPath(source, current.Path) {
		return true
	}
	name := filepath.Base(source)
	digest, ok := strings.CutPrefix(name, "crewship-agent-cli-")
	if _, err := hex.DecodeString(digest); !ok || len(digest) != 64 || err != nil {
		return false
	}
	local := filepath.Join(filepath.Dir(current.Path), name)
	if !p.matchesDaemonPath(source, local) {
		return false
	}
	previous := &runtimestage.AgentCLIArtifact{Path: local, SHA256: digest, Arch: current.Arch}
	return runtimestage.VerifyAgentCLIArtifact(previous) == nil
}

func (p *Provider) mappedVolumeOverlapsStorage(path string) bool {
	for _, root := range []string{p.pathMap.local, p.pathMap.daemon} {
		if pathContains(root, path) || pathContains(path, root) {
			return true
		}
	}
	return false
}

func (p *Provider) checkMappedManagedVolume(v volume.Volume, name, crewID string) error {
	if v.Name != name || v.Driver != "local" || !cleanAbsolutePath(v.Mountpoint) || len(v.Options) != 0 || p.mappedVolumeOverlapsStorage(v.Mountpoint) {
		return fmt.Errorf("%w: home or tools volume is not daemon managed", errMappedRuntimeMount)
	}
	if owner := v.Labels[resourcelifecycle.InstanceLabel]; owner != "" && owner != p.cfg.InstanceID {
		return fmt.Errorf("%w: home or tools volume belongs to another installation", errMappedRuntimeMount)
	}
	if owner := v.Labels[crewCrewIDLabel]; owner != "" && owner != crewID {
		return fmt.Errorf("%w: home or tools volume belongs to another crew", errMappedRuntimeMount)
	}
	return nil
}

// Refusal of a provably owned runtime disables Docker's autonomous restart.
// Transient daemon/inspect errors remain fail-closed without stopping work.
func (p *Provider) stopMappedRuntimeOnRefusal(ctx context.Context, team provider.CrewConfig, inspected container.InspectResponse, cause error) error {
	if !errors.Is(cause, errMappedRuntimeMount) || p.cfg.InstanceID == "" || inspected.Config == nil ||
		inspected.ID == "" || inspected.State == nil {
		return cause
	}
	labels := inspected.Config.Labels
	if labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID ||
		labels[crewKindLabel] != crewRuntimeKind || labels[crewCrewIDLabel] != team.ID {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	p.evictWarm(team.ID)
	p.fenced.Delete(inspected.ID)
	policy := container.RestartPolicy{Name: container.RestartPolicyDisabled}
	_, updateErr := p.client.ContainerUpdate(ctx, inspected.ID, client.ContainerUpdateOptions{RestartPolicy: &policy})
	if !inspected.State.Running && !inspected.State.Restarting {
		return errors.Join(cause, updateErr)
	}
	timeout := 5
	_, stopErr := p.client.ContainerStop(ctx, inspected.ID, client.ContainerStopOptions{Timeout: &timeout})
	return errors.Join(cause, updateErr, stopErr)
}

func (p *Provider) startMappedCrewContainer(ctx context.Context, id string, team provider.CrewConfig) error {
	if p.pathMap != nil {
		result, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("inspect mapped runtime before start: %w", err)
		}
		if err := p.checkMappedRuntimeMounts(ctx, result.Container.Mounts, team); err != nil {
			return err
		}
	}
	_, err := p.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}
