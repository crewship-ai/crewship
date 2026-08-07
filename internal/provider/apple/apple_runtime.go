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

	// Any cached run-as user for this name is dropped for the whole call. The
	// name is the cache key (on Apple Containers configuration.id IS the name),
	// and this function may `rm` the container behind it and build a new one
	// from a different image — so the answer read before the call cannot be
	// assumed to describe the container that exists after it. Deferred rather
	// than done inline so every return path, including the reuse and race
	// branches, is covered.
	defer p.forgetContainerUser(containerName)

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
		// The deferred forget above is keyed on the name we asked for; if the
		// runtime hands back a different id, drop that one too rather than
		// leave a previous container's user cached under it.
		defer p.forgetContainerUser(containerID)
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
	if hostDir, mountRoot, ok := p.hostPathAndRootFor(containerID, dstPath); ok {
		for _, name := range entries {
			dst := filepath.Join(hostDir, filepath.FromSlash(name))
			if err := p.makeReachableDirs(mountRoot, filepath.Dir(dst)); err != nil {
				return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
			}
			data, err := os.ReadFile(filepath.Join(staging, name)) // #nosec G304 — staging path
			if err != nil {
				return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
			}
			if err := os.WriteFile(dst, data, 0o600); err != nil {
				return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
			}
			if err := p.giveAgentAccess(dst, 0o644); err != nil {
				return fmt.Errorf("copy %s into %s: %w", name, dstPath, err)
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

// hostPathAndRootFor maps a container path to its host side, or reports false
// when it is not inside a known bind mount.
//
// It returns the host side of the MOUNT as well, which is the boundary for
// anything that has to walk upwards: the mapped path may be several
// directories below it, and those directories are the ones a copy has to
// create — and make reachable.
func (p *Provider) hostPathAndRootFor(containerID, containerPath string) (hostPath, mountRoot string, ok bool) {
	p.mountsMu.RLock()
	defer p.mountsMu.RUnlock()

	clean := path.Clean(containerPath)
	for cPath, hPath := range p.mounts[containerID] {
		cPath = path.Clean(cPath)
		if clean == cPath {
			return hPath, hPath, true
		}
		if strings.HasPrefix(clean, cPath+"/") {
			return filepath.Join(hPath, filepath.FromSlash(strings.TrimPrefix(clean, cPath+"/"))), hPath, true
		}
	}
	return "", "", false
}

// unpackTarInto writes the archive's regular files under root and returns
// their names, refusing any entry that would land outside it.
//
// The archive is rendered from operator-supplied config, so a `../` or
// absolute entry is a path-traversal attempt against the host. The staging
// directory is the boundary, and it takes all three layers internal/safepath
// prescribes to actually hold it:
//
//   - safepath.JoinRel for lexical confinement. A hand-rolled path.Clean
//     check is not equivalent: path.Clean does not treat a backslash as a
//     separator on unix, so `..\escaped` survives it and then escapes once
//     the same archive is unpacked on Windows.
//   - a cumulative byte cap. A per-entry limit alone does not bound an
//     archive — 40 entries just under a 32 MB cap still write 1.2 GB. This
//     is the lesson devcontainer/features.go records as Audit M24.
//   - an *os.Root for the write itself. The lexical layer is textual and
//     cannot see a symlink: if a directory inside the staging tree is a
//     symlink pointing out, `out/escaped` is textually in-bounds and still
//     lands outside. os.Root resolves one component at a time and refuses
//     to follow a link leaving the root, which is what closes that hole.
func unpackTarInto(root string, content io.Reader) ([]string, error) {
	return unpackTarIntoLimited(root, content, maxCopyEntryBytes, maxCopyTotalBytes)
}

// unpackTarIntoLimited is unpackTarInto with the caps injected. Exercising the
// caps at their production values means *building* an archive that exceeds
// them, and a tar writer has to materialise every declared byte — the first
// version of the cumulative-cap test allocated 3.7 GB to prove a limit of
// 256 MB, which is fine on a workstation and fatal on a CI runner compiling
// several packages at once.
func unpackTarIntoLimited(root string, content io.Reader, maxEntry, maxTotal int64) ([]string, error) {
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("opening staging root: %w", err)
	}
	defer func() { _ = rootFS.Close() }()

	var (
		names []string
		total int64
	)
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
			continue // directories are created below; links never travel
		}

		// Lexical confinement. JoinRel validates every segment of the
		// untrusted name before the join, so traversal is provably
		// rejected rather than merely cleaned away.
		abs, err := safepath.JoinRel(root, hdr.Name)
		if err != nil {
			return nil, fmt.Errorf("refusing archive entry %q: %w", hdr.Name, err)
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == "." {
			return nil, fmt.Errorf("refusing archive entry %q: %w", hdr.Name, safepath.ErrUnsafe)
		}

		// Compare against the declared size before writing: it is the
		// upper bound on what the bounded copy below can produce.
		if hdr.Size > maxEntry {
			return nil, fmt.Errorf("archive entry %q exceeds the per-entry cap (%d > %d)",
				hdr.Name, hdr.Size, maxEntry)
		}
		if total+hdr.Size > maxTotal {
			return nil, fmt.Errorf("archive exceeds the cumulative size cap (%d > %d) at entry %q",
				total+hdr.Size, maxTotal, hdr.Name)
		}

		if dir := filepath.Dir(rel); dir != "." {
			if err := rootFS.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("staging %s: %w", rel, err)
			}
		}
		mode := os.FileMode(hdr.Mode).Perm() // #nosec G115 — tar mode is bounded
		if mode == 0 {
			mode = 0o600
		}
		f, err := rootFS.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return nil, fmt.Errorf("staging %s: %w", rel, err)
		}
		// Bounded copy: an archive is not a reason to fill the host disk.
		// Read one byte past the cap so an entry that lies about its size in
		// the header is caught here rather than silently truncated. A
		// half-written .mcp.json is worse than a refusal: the copy reports
		// success and the consumer fails later on a parse error that names
		// nothing about the real cause.
		n, err := io.Copy(f, io.LimitReader(tr, maxEntry+1))
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("staging %s: %w", rel, err)
		}
		if n > maxEntry {
			_ = f.Close()
			return nil, fmt.Errorf("archive entry %q streamed more than its declared size and exceeds the per-entry cap (%d)",
				hdr.Name, maxEntry)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("staging %s: %w", rel, err)
		}
		total += n
		names = append(names, rel)
	}
	return names, nil
}

// giveAgentAccess hands a path over to the agent uid, or — when it cannot —
// widens its mode so the agent can still use it.
//
// The agent runs as 1001:1001. Setting that owner needs root, which the server
// does not have on a normal macOS install, so the chown usually fails, and a
// 0700/0600 path owned by the server user is one the agent cannot use.
// Returning nil there reproduces the "config file not found" symptom this whole
// path exists to fix, with nothing anywhere to act on.
//
// The widened mode is a deliberate trade: on a host with other human users they
// can read a crew's config. That is worse than correct ownership and better
// than a crew that cannot start, and it only happens where the chown failed —
// i.e. where the alternative is a file nobody can read at all.
func (p *Provider) giveAgentAccess(path string, widened os.FileMode) error {
	if err := p.chown(path, agentUID, agentGID); err == nil {
		return nil
	} else if chmodErr := os.Chmod(path, widened); chmodErr != nil {
		return fmt.Errorf("could not give the agent access to %s (chown: %v; chmod: %w)", path, err, chmodErr)
	} else {
		p.logger.Debug("could not chown to the agent user; widened the mode instead",
			"path", path, "mode", widened.String(), "error", err)
	}
	return nil
}

// makeReachableDirs creates dir under base and makes every directory it had to
// create reachable by the agent.
//
// Widening the file alone is not enough: reaching /crew/agents/<slug>/.mcp.json
// needs execute on <slug> too, and that directory is created here, owned by the
// server user, mode 0750. Without this the agent gets EACCES on the traversal
// and the copy still reports success — the same defect one level up from the
// one the file-level fallback closes.
//
// Only newly created directories are touched. An existing directory belongs to
// whoever made it, and silently relaxing its mode would be a different kind of
// surprise.
func (p *Provider) makeReachableDirs(base, dir string) error {
	var created []string
	for cur := dir; strings.HasPrefix(cur, base) && cur != base; cur = filepath.Dir(cur) {
		if _, err := os.Stat(cur); err == nil {
			break // this one and everything above it already exists
		}
		created = append(created, cur)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// Outermost first, so a failure leaves the shallowest path fixed rather
	// than an unreachable leaf.
	for i := len(created) - 1; i >= 0; i-- {
		if err := p.giveAgentAccess(created[i], 0o755); err != nil {
			return err
		}
	}
	return nil
}

// maxCopyEntryBytes caps a single staged file and maxCopyTotalBytes the
// archive as a whole. Config files are kilobytes; both are only here so a
// malformed or hostile archive cannot fill the disk.
const (
	maxCopyEntryBytes = 32 << 20
	maxCopyTotalBytes = 256 << 20
)

// Close stops the background gc goroutine and releases resources.
func (p *Provider) Close() error {
	close(p.done)
	return nil
}

// runCLI executes `container <args...>` and returns stdout bytes.

// chown is os.Chown unless a test replaced it. Nil-safe so every existing
// Provider literal keeps working without naming the field.
func (p *Provider) chown(name string, uid, gid int) error {
	if p.chownFn != nil {
		return p.chownFn(name, uid, gid)
	}
	return os.Chown(name, uid, gid)
}
