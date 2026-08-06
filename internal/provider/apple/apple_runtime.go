package apple

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"archive/tar"
	"errors"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/safepath"
	"path"
)

func (p *Provider) EnsureCrewRuntime(ctx context.Context, team provider.CrewConfig) (string, error) {
	p.logger.Debug("EnsureCrewRuntime", "crew_id", team.ID, "crew_slug", team.Slug)

	// crew_id/slug end up as filesystem path components below — validate
	// before any filepath.Join so a malformed ID can't reach the bind
	// mount layer (which would let an attacker who controls the DB pin
	// container output at /etc, /root, etc.).
	if _, err := safepath.ValidateComponent(team.ID); err != nil {
		return "", fmt.Errorf("crew id not safe for path: %w", err)
	}
	if _, err := safepath.ValidateComponent(team.Slug); err != nil {
		return "", fmt.Errorf("crew slug not safe for path: %w", err)
	}

	// Say what this provider will not do with the config it was handed, before
	// it does anything at all (#1648). Ahead of the container lookup as well as
	// the create, because a crew flipped to restricted egress while its
	// container was already up would otherwise keep being handed back unfenced
	// on the reuse path, which never reaches the create branch again.
	if support := p.UnsupportedCrewConfig(team); !support.Empty() {
		if err := support.RefusedError(providerName); err != nil {
			p.logger.Error("refusing crew config this provider cannot apply",
				"crew_id", team.ID, "crew_slug", team.Slug, "fields", support.Fields(), "error", err)
			return "", err
		}
		p.logger.Warn("crew config fields not honoured by this provider",
			"crew_id", team.ID, "crew_slug", team.Slug,
			"fields", support.Fields(),
			"detail", strings.Join(support.DegradedMessages(), " | "))
	}

	if p.cfg.Network != "" {
		if err := p.ensureNetwork(ctx, p.cfg.Network); err != nil {
			return "", fmt.Errorf("ensure network: %w", err)
		}
	}

	containerName := p.CrewContainerName(team.ID, team.Slug)

	// Check if container already exists
	existing, err := p.findContainer(ctx, containerName)
	if err == nil && existing != nil {
		if existing.State() == "running" {
			// Reuse returns before the create block, so the mounts have to be
			// registered here as well — after a server restart the map is
			// empty and every write would fall back to the broken CLI copy.
			p.rememberBindMounts(existing.Configuration.ID, map[string]string{
				"/crew":      filepath.Join(p.cfg.OutputBasePath, "crews", team.ID),
				"/workspace": filepath.Join(p.cfg.OutputBasePath, "workspaces", team.ID),
				"/output":    filepath.Join(p.cfg.OutputBasePath, team.ID),
			})
			return existing.Configuration.ID, nil
		}
		// Verify bind-mount directories still exist (macOS /tmp is wiped on reboot).
		bindMountDirs := []string{
			filepath.Join(p.cfg.OutputBasePath, "workspaces", team.ID),
			filepath.Join(p.cfg.OutputBasePath, team.ID),
			filepath.Join(p.cfg.OutputBasePath, "crews", team.ID),
		}
		bindsMissing := false
		for _, d := range bindMountDirs {
			if _, statErr := os.Stat(d); os.IsNotExist(statErr) {
				bindsMissing = true
				break
			}
		}
		if bindsMissing {
			p.logger.Info("bind-mount dirs missing, recreating container", "container", containerName)
			_, _ = runCLI(ctx, "rm", existing.Configuration.ID)
			// fall through to create a fresh container below
		} else {
			// Admission control (#1668): restarting a stopped container puts
			// a full lightweight VM back on the host, so it is gated exactly
			// like a create. The already-running branch above returned before
			// reaching here and is therefore free.
			releaseSlot, admitErr := p.admitContainerStart(ctx, team)
			if admitErr != nil {
				return "", admitErr
			}
			defer releaseSlot()

			// Start stopped container
			if _, err := runCLI(ctx, "start", existing.Configuration.ID); err != nil {
				return "", fmt.Errorf("start existing container: %w", err)
			}
			return existing.Configuration.ID, nil
		}
	}

	// Set up resources
	memoryMB := team.MemoryMB
	if memoryMB == 0 {
		memoryMB = 512
	}
	cpus := team.CPUs
	if cpus == 0 {
		cpus = 1.0
	}

	// Admission control (#1668). Past this point we are adding a container to
	// the host; everything above either reused one or decided one must be
	// built. Ahead of the image pull deliberately — work started before the
	// slot is held is work done on a host that may still say no.
	releaseSlot, err := p.admitContainerStart(ctx, team)
	if err != nil {
		return "", err
	}
	defer releaseSlot()

	image := p.crewImage(team)
	if err := p.ensureImage(ctx, image); err != nil {
		return "", fmt.Errorf("ensure image: %w", err)
	}

	// Create host directories for bind mounts
	outputPath := filepath.Join(p.cfg.OutputBasePath, team.ID)
	if err := os.MkdirAll(outputPath, 0750); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	workspacePath := filepath.Join(p.cfg.OutputBasePath, "workspaces", team.ID)
	if err := os.MkdirAll(workspacePath, 0750); err != nil {
		return "", fmt.Errorf("create workspace dir: %w", err)
	}
	if err := os.Chown(workspacePath, 1001, 1001); err != nil {
		p.logger.Debug("chown workspace (non-fatal)", "path", workspacePath, "error", err)
	}

	crewPath := filepath.Join(p.cfg.OutputBasePath, "crews", team.ID)
	for _, sub := range []string{"shared", "agents"} {
		if err := os.MkdirAll(filepath.Join(crewPath, sub), 0750); err != nil {
			return "", fmt.Errorf("create crew dir %s: %w", sub, err)
		}
	}
	for _, dir := range []string{crewPath, filepath.Join(crewPath, "shared"), filepath.Join(crewPath, "agents")} {
		if err := os.Chown(dir, 1001, 1001); err != nil {
			p.logger.Debug("chown crew dir (non-fatal)", "path", dir, "error", err)
		}
	}

	// Build create command
	// Apple Container CLI requires integer CPUs (no fractional)
	cpuInt := int(cpus)
	if cpuInt < 1 {
		cpuInt = 1
	}
	args, err := buildCreateArgs(createArgsInput{
		containerName:  containerName,
		image:          image,
		network:        p.cfg.Network,
		cpus:           cpuInt,
		memoryMB:       memoryMB,
		crewID:         team.ID,
		workspacePath:  workspacePath,
		outputPath:     outputPath,
		crewPath:       crewPath,
		sidecarPath:    p.cfg.SidecarBinaryPath,
		entrypointPath: p.cfg.EntrypointPath,
		containerEnv:   team.ContainerEnv,
	})
	if err != nil {
		return "", err
	}

	out, err := runCLI(ctx, args...)
	if err != nil {
		// Handle race condition: another goroutine created the container concurrently
		if strings.Contains(err.Error(), "already exists") {
			existing, findErr := p.findContainer(ctx, containerName)
			if findErr == nil && existing != nil {
				if existing.State() != "running" {
					if _, startErr := runCLI(ctx, "start", existing.Configuration.ID); startErr != nil {
						return "", fmt.Errorf("start existing container after race: %w", startErr)
					}
				}
				return existing.Configuration.ID, nil
			}
		}
		return "", fmt.Errorf("container create: %w (output: %s)", err, string(out))
	}

	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		containerID = containerName
	}

	// Start the container
	if _, err := runCLI(ctx, "start", containerID); err != nil {
		return "", fmt.Errorf("container start: %w", err)
	}

	// Get actual container ID from inspect
	info, err := p.inspectContainer(ctx, containerID)
	if err == nil && info.Configuration.ID != "" {
		containerID = info.Configuration.ID
		// Discover host IP from gateway if not already known
		p.mu.Lock()
		if p.hostIP == "" && info.GatewayIP() != "" {
			p.hostIP = info.GatewayIP()
		}
		p.mu.Unlock()
	}

	p.logger.Info("crew container started",
		"crew_id", team.ID,
		"container_id", provider.ShortID(containerID),
	)

	// Record the mounts so CopyToContainer can write through them rather than
	// through `container cp`, which is a silent no-op into a mounted path.
	p.rememberBindMounts(containerID, map[string]string{
		"/crew":      crewPath,
		"/workspace": workspacePath,
		"/output":    outputPath,
	})

	return containerID, nil
}

func (p *Provider) RemoveCrewVolumes(_ context.Context, id, slug string) error {
	// Namespace the per-crew home directory by the globally-unique crew id
	// (audit C1) rather than the per-workspace slug, so one tenant can never
	// target another's home path on a shared host.
	if _, err := safepath.ValidateComponent(id); err != nil {
		return fmt.Errorf("crew id not safe for path: %w", err)
	}
	homePath := filepath.Join(p.cfg.OutputBasePath, "homes", id)
	if err := os.RemoveAll(homePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove crew home %s: %w", id, err)
	}
	return nil
}

// pipeReadWriteCloser wraps separate read/write pipes into a single io.ReadWriteCloser.

// CopyToContainer unpacks a tar archive into dstPath inside the container.
//
// This was a stub, and that is what kept agents from running on macOS even
// after the container itself started: the orchestrator writes
// /crew/agents/<slug>/.mcp.json through this call, the write failed, and
// claude-code exited 1 with "MCP config file not found" (#1779).
//
// The contract's tar comes from Docker's API shape. Apple's CLI takes no tar —
// `container cp <src> <container>:<dst>` moves paths — so the archive is
// staged on the host and its entries copied in. The staging directory is
// removed on every exit path: it can hold rendered MCP config with credential
// references in it.
func (p *Provider) CopyToContainer(ctx context.Context, containerID string, dstPath string, content io.Reader) error {
	staging, err := os.MkdirTemp("", "crewship-cp-*")
	if err != nil {
		return fmt.Errorf("staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	entries, err := unpackTarInto(staging, content)
	if err != nil {
		return err
	}

	// Prefer the host side of a bind mount. `container cp` into a mounted path
	// is a silent no-op on 1.2.0 — exit 0, destination echoed, nothing copied —
	// and /crew, /workspace and /output are mounts this provider creates, so
	// their host paths are known and a plain write is both simpler and
	// checkable. The CLI stays the fallback for anything unmounted.
	if hostDir, ok := p.hostPathFor(containerID, dstPath); ok {
		for _, name := range entries {
			dst := filepath.Join(hostDir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
			}
			data, err := os.ReadFile(filepath.Join(staging, name)) // #nosec G304 — staging path
			if err != nil {
				return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
			}
			if err := os.WriteFile(dst, data, 0o600); err != nil {
				return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
			}
			// The agent runs as 1001:1001 and must be able to read it.
			if err := os.Chown(dst, agentUID, agentGID); err != nil {
				p.logger.Warn("could not chown copied file to the agent user", "path", dst, "error", err)
			}
		}
		return nil
	}

	for _, name := range entries {
		src := filepath.Join(staging, name)
		dst := containerID + ":" + path.Join(dstPath, name)
		if _, err := runCLI(ctx, "cp", src, dst); err != nil {
			return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
		}
	}
	return nil
}

// agentUID/agentGID are the uid:gid the crew container runs as.
const (
	agentUID = 1001
	agentGID = 1001
)

// rememberBindMounts records a container's container-path → host-path mounts so
// CopyToContainer can write through them.
func (p *Provider) rememberBindMounts(containerID string, mounts map[string]string) {
	p.mountsMu.Lock()
	defer p.mountsMu.Unlock()
	if p.mounts == nil {
		p.mounts = make(map[string]map[string]string)
	}
	p.mounts[containerID] = mounts
}

// hostPathFor maps a container path to its host side, or reports false when it
// is not inside a known bind mount.
func (p *Provider) hostPathFor(containerID, containerPath string) (string, bool) {
	p.mountsMu.RLock()
	defer p.mountsMu.RUnlock()

	clean := path.Clean(containerPath)
	for cPath, hPath := range p.mounts[containerID] {
		cPath = path.Clean(cPath)
		if clean == cPath {
			return hPath, true
		}
		if strings.HasPrefix(clean, cPath+"/") {
			return filepath.Join(hPath, filepath.FromSlash(strings.TrimPrefix(clean, cPath+"/"))), true
		}
	}
	return "", false
}

// unpackTarInto writes the archive's regular files under root and returns
// their names, refusing any entry that would land outside it.
//
// The archive is rendered from operator-supplied config, so a `../` or
// absolute entry is a path-traversal attempt against the host — the staging
// directory is the boundary and it is enforced here rather than trusted.
func unpackTarInto(root string, content io.Reader) ([]string, error) {
	var names []string
	tr := tar.NewReader(content)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue // directories are created by the copy; nothing else travels
		}
		clean := path.Clean("/" + hdr.Name)[1:] // strips ../ and any leading /
		if clean == "" || clean != hdr.Name {
			return nil, fmt.Errorf("refusing archive entry %q: escapes the destination", hdr.Name)
		}
		target := filepath.Join(root, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, fmt.Errorf("staging %s: %w", clean, err)
		}
		mode := os.FileMode(hdr.Mode).Perm() // #nosec G115 — tar mode is bounded
		if mode == 0 {
			mode = 0o600
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) // #nosec G304 — target is under root
		if err != nil {
			return nil, fmt.Errorf("staging %s: %w", clean, err)
		}
		// Bounded copy: an archive is not a reason to fill the host disk.
		if _, err := io.Copy(f, io.LimitReader(tr, maxCopyEntryBytes)); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("staging %s: %w", clean, err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("staging %s: %w", clean, err)
		}
		names = append(names, clean)
	}
	return names, nil
}

// maxCopyEntryBytes caps a single staged file. Config files are kilobytes;
// this is only here so a malformed archive cannot fill the disk.
const maxCopyEntryBytes = 32 << 20

// Close stops the background gc goroutine and releases resources.
func (p *Provider) Close() error {
	close(p.done)
	return nil
}

// runCLI executes `container <args...>` and returns stdout bytes.
