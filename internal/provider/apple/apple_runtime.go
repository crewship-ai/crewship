package apple

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

func (p *Provider) EnsureCrewRuntime(ctx context.Context, team provider.CrewConfig) (string, error) {
	p.logger.Debug("EnsureCrewRuntime", "crew_id", team.ID, "crew_slug", team.Slug)

	if p.cfg.Network != "" {
		if err := p.ensureNetwork(ctx, p.cfg.Network); err != nil {
			return "", fmt.Errorf("ensure network: %w", err)
		}
	}

	containerName := p.CrewContainerName(team.Slug)

	// Check if container already exists
	existing, err := p.findContainer(ctx, containerName)
	if err == nil && existing != nil {
		if existing.Status == "running" {
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

	if err := p.ensureImage(ctx, p.cfg.RuntimeImage); err != nil {
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
	args := []string{
		"create",
		"--name", containerName,
		"--cpus", fmt.Sprintf("%d", cpuInt),
		"--memory", fmt.Sprintf("%dM", memoryMB),
		"--read-only",
		"--env", "CREWSHIP_CREW_ID=" + team.ID,
		"-v", workspacePath + ":/workspace",
		"-v", outputPath + ":/output",
		"-v", crewPath + ":/crew",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home/agent",
	}

	if p.cfg.Network != "" {
		args = append(args, "--network", p.cfg.Network)
	}

	// Apple Containers use --user for the init process user
	args = append(args, "--user", "1001:1001")

	// Image + entrypoint: keep container alive
	args = append(args, p.cfg.RuntimeImage, "sleep", "infinity")

	out, err := runCLI(ctx, args...)
	if err != nil {
		// Handle race condition: another goroutine created the container concurrently
		if strings.Contains(err.Error(), "already exists") {
			existing, findErr := p.findContainer(ctx, containerName)
			if findErr == nil && existing != nil {
				if existing.Status != "running" {
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
		if p.hostIP == "" && len(info.Networks) > 0 && info.Networks[0].IPv4Gateway != "" {
			p.hostIP = info.Networks[0].IPv4Gateway
		}
		p.mu.Unlock()
	}

	p.logger.Info("crew container started",
		"crew_id", team.ID,
		"container_id", shortID(containerID),
	)

	return containerID, nil
}

func (p *Provider) RemoveCrewVolumes(_ context.Context, slug string) error {
	homePath := filepath.Join(p.cfg.OutputBasePath, "homes", slug)
	if err := os.RemoveAll(homePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove crew home %s: %w", slug, err)
	}
	return nil
}

// pipeReadWriteCloser wraps separate read/write pipes into a single io.ReadWriteCloser.

func (p *Provider) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return fmt.Errorf("CopyToContainer not supported on Apple Containers provider")
}

// Close stops the background gc goroutine and releases resources.
func (p *Provider) Close() error {
	close(p.done)
	return nil
}

// runCLI executes `container <args...>` and returns stdout bytes.
