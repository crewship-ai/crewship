package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
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

// localBindSource authenticates an inspect path in the server filesystem. Paths
// outside the configured mapping cannot be authenticated from this process.
func (p *Provider) localBindSource(source string) (string, error) {
	if p.pathMap == nil {
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
