package docker

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// Numeric host ownership cannot isolate an SDK generation from an agent with
// the same UID. Before create, reject configured writable bind aliases of its
// private tree. Runtime admission must also check actual mounts and volume
// backing paths; this configuration check alone does not prove custody.
func (p *Provider) validateAgentCLIBindSources(mounts []mount.Mount) error {
	if p.cfg.agentCLIArtifact == nil {
		return nil
	}
	protected, err := prospectiveHostPath(filepath.Dir(p.cfg.agentCLIArtifact.Path))
	if err != nil {
		return fmt.Errorf("resolve trusted agent CLI tree: %w", err)
	}
	for _, m := range mounts {
		if m.ReadOnly {
			continue
		}
		rawSource := ""
		switch m.Type {
		case mount.TypeBind:
			rawSource = m.Source
		case mount.TypeVolume:
			// Persistent crew data uses anonymous local volumes with an explicit
			// bind device to enforce noexec. Checking only TypeBind misses these.
			if m.VolumeOptions != nil && m.VolumeOptions.DriverConfig != nil {
				driver := m.VolumeOptions.DriverConfig
				if driver.Name != "local" || driver.Options["type"] != "none" {
					return errors.New("native agent CLI cannot authenticate the configured writable volume driver")
				}
				rawSource = driver.Options["device"]
			}
		}
		if rawSource == "" {
			continue
		} // named volumes require actual inspect
		source, err := prospectiveHostPath(rawSource)
		if err != nil {
			return fmt.Errorf("authenticate writable mount before agent CLI delivery: %w", err)
		}
		if pathContains(source, protected) || pathContains(protected, source) {
			return errors.New("native agent CLI staging is exposed through a writable crew bind")
		}
	}
	return nil
}

var agentSDKCommandPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*( [a-z][a-z0-9-]*)*$`)

func validateAgentCLIProbe(probe buildinfo.AgentRuntimeProbe, artifact *runtimestage.AgentCLIArtifact, expectedCommit string, required []string) error {
	denied := errors.New("native agent CLI identity or required capabilities are unqualified; reinstall matching Crewship runtime companions")
	if artifact == nil || probe.Protocol != 1 || probe.OS != "linux" || probe.Arch != artifact.Arch || probe.SHA256 != artifact.SHA256 || len(probe.SHA256) != 64 || len(probe.Commands) > 32 {
		return denied
	}
	if _, err := hex.DecodeString(probe.SHA256); err != nil {
		return denied
	}
	knownCommit := func(value string) bool {
		if len(value) < 7 || len(value) > 40 {
			return false
		}
		for _, r := range value {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	if !knownCommit(expectedCommit) || !knownCommit(probe.Commit) || (!strings.HasPrefix(probe.Commit, expectedCommit) && !strings.HasPrefix(expectedCommit, probe.Commit)) {
		return denied
	}
	if probe.Version == "" || len(probe.Version) > 256 || strings.ContainsAny(probe.Version, "\x00\r\n") {
		return denied
	}
	available := make(map[string]bool, len(probe.Commands))
	for _, path := range probe.Commands {
		if len(path) > 256 || !agentSDKCommandPattern.MatchString(path) || available[path] {
			return denied
		}
		available[path] = true
	}
	for _, path := range required {
		if !available[path] {
			return denied
		}
	}
	return nil
}

// qualifyAgentCLI checks actual Docker mounts and the executing client before
// the runtime is advertised ready. Metadata labels alone are not this proof.
func (p *Provider) qualifyAgentCLI(parent context.Context, id string) error {
	if p.cfg.agentCLIArtifact == nil {
		if p.cfg.agentCLIError != nil {
			return fmt.Errorf("native agent CLI staging failed: %w", p.cfg.agentCLIError)
		}
		if p.cfg.RequireAgentCLI {
			return errors.New("native agent CLI companion is missing; reinstall the complete Crewship distribution")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	denied := errors.New("native agent CLI requires its current read-only mount in a qualified crew runtime")
	got, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect native agent CLI runtime: %w", err)
	}
	c := got.Container
	if c.ID != id || c.Config == nil || c.HostConfig == nil || !c.HostConfig.ReadonlyRootfs || c.State == nil || !c.State.Running || c.State.Paused || c.State.Restarting || provider.IsPrivilegedExecUser(c.Config.User) {
		return denied
	}
	if c.HostConfig.Privileged || len(c.HostConfig.CapAdd) != 0 || !slices.Contains(c.HostConfig.CapDrop, "ALL") || (!slices.Contains(c.HostConfig.SecurityOpt, "no-new-privileges") && !slices.Contains(c.HostConfig.SecurityOpt, "no-new-privileges:true")) {
		return denied
	}
	const executable = agentCLIExecutable
	bound := false
	var writable []container.MountPoint
	for _, m := range c.Mounts {
		if m.Destination == executable {
			if bound || m.Type != mount.TypeBind || m.RW || m.Source != p.cfg.agentCLIArtifact.Path {
				return denied
			}
			bound = true
		} else if !path.IsAbs(m.Destination) || path.Clean(m.Destination) != m.Destination || covers(m.Destination, executable) {
			return denied
		}
		if m.RW {
			writable = append(writable, m)
		}
	}
	if !bound {
		return denied
	}
	for target := range c.HostConfig.Tmpfs {
		if !path.IsAbs(target) || path.Clean(target) != target || covers(target, executable) {
			return denied
		}
	}
	if err := p.hostMountTreeHidden(ctx, writable, filepath.Dir(p.cfg.agentCLIArtifact.Path), true); err != nil {
		return fmt.Errorf("native agent CLI staging is not private from writable crew mounts: %w", err)
	}
	if err := runtimestage.VerifyAgentCLIArtifact(p.cfg.agentCLIArtifact); err != nil {
		return err
	}
	for _, target := range []string{"/usr", "/usr/local", "/usr/local/bin", executable} {
		stat, err := p.client.ContainerStatPath(ctx, id, client.ContainerStatPathOptions{Path: target})
		if err != nil || stat.Stat.LinkTarget != "" {
			return denied
		}
		if target == executable {
			if !stat.Stat.Mode.IsRegular() || stat.Stat.Mode.Perm() != 0555 {
				return denied
			}
		} else if !stat.Stat.Mode.IsDir() {
			return denied
		}
	}
	image, err := p.client.ImageInspect(ctx, c.Image)
	if err != nil {
		return fmt.Errorf("inspect native agent CLI target platform: %w", err)
	}
	if image.Os != "linux" || image.Architecture != p.cfg.agentCLIArtifact.Arch {
		return errors.New("native agent CLI companion does not match the crew image platform")
	}
	result, err := p.Exec(ctx, provider.ExecConfig{ContainerID: id, Cmd: []string{executable, "agent-runtime-probe"}, WorkingDir: "/", User: c.Config.User, Env: []string{"CREWSHIP_CONFIG=/dev/null", "CREWSHIP_PROFILE=", "GODEBUG="}})
	if err != nil {
		return fmt.Errorf("native agent CLI probe could not start: %w", err)
	}
	defer result.Reader.Close()
	type readResult struct {
		data []byte
		err  error
	}
	finished := make(chan readResult, 1)
	go func() {
		raw, err := io.ReadAll(io.LimitReader(result.Reader, (64<<10)+1))
		finished <- readResult{raw, err}
	}()
	var raw []byte
	select {
	case read := <-finished:
		if read.err != nil || len(read.data) > 64<<10 {
			return errors.New("bounded native agent CLI probe unavailable")
		}
		raw = read.data
	case <-ctx.Done():
		result.Reader.Close()
		return fmt.Errorf("native agent CLI probe did not settle: %w", ctx.Err())
	}
	code, running, err := p.waitExecExit(ctx, result.ExecID, 20)
	if err != nil || running || code != 0 {
		return errors.New("native agent CLI probe did not confirm successful completion")
	}
	var probe buildinfo.AgentRuntimeProbe
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&probe) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("native agent CLI returned an invalid probe protocol")
	}
	return validateAgentCLIProbe(probe, p.cfg.agentCLIArtifact, p.cfg.ExpectedAgentCLICommit, p.cfg.RequiredAgentCLICommands)
}
