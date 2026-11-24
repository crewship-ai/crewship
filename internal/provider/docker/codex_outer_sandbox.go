package docker

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/safepath"
)

// AttestCodexOuterSandbox is a stronger, separate qualification than the
// managed-launch pilot. It does not change host policy or container settings.
func (p *Provider) AttestCodexOuterSandbox(ctx context.Context, id, crew string, d managedlaunch.Descriptor) error {
	denied := errors.New("Codex outer isolation: actual runtime security controls or mounts are unqualified; recreate a supported crew runtime")
	_, componentErr := safepath.ValidateComponent(crew)
	if p.cfg.InstanceID == "" || componentErr != nil {
		return denied
	}
	if err := p.AttestManagedLaunch(ctx, id, d); err != nil {
		return err
	}
	return p.attestCodexOuterMetadata(ctx, id, crew, d)
}

// Split metadata qualification from launcher trust for deterministic fixtures.
// Production always enters through the full attestation above.
func (p *Provider) attestCodexOuterMetadata(ctx context.Context, id, crew string, d managedlaunch.Descriptor) error {
	denied := errors.New("Codex outer isolation: actual runtime security controls or mounts are unqualified; recreate a supported crew runtime")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	got, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return denied
	}
	c := got.Container
	h := c.HostConfig
	if c.ID != id || dockerutil.ConfigImageID(c.Image) != d.ImageID || c.State == nil || !c.State.Running || c.Config == nil || h == nil {
		return denied
	}
	labels := c.Config.Labels
	if labels[crewCrewIDLabel] != crew || labels[crewKindLabel] != crewRuntimeKind || labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || c.Config.User != "1001:1001" {
		return denied
	}
	if !h.ReadonlyRootfs || h.Privileged || len(h.CapAdd) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 || len(h.VolumesFrom) != 0 {
		return denied
	}
	if h.PidMode != "" || (h.IpcMode != "" && h.IpcMode != "private") || h.UTSMode != "" || h.UsernsMode != "" || h.CgroupnsMode == "host" || h.NetworkMode == "host" || strings.HasPrefix(string(h.NetworkMode), "container:") {
		return denied
	}
	if c.AppArmorProfile != "docker-default" {
		return denied
	}
	nnp := false
	for _, option := range h.SecurityOpt {
		switch option {
		case "no-new-privileges", "no-new-privileges:true":
			nnp = true
		default:
			return denied
		}
	}
	if !nnp {
		return denied
	}
	// The only writable host-backed locations are these crew-owned roots.
	expected := map[string][]string{"/output": {crew}, "/workspace": {"workspaces", crew}, "/crew": {"crews", crew}}
	seen := map[string]bool{}
	// Native Vault delivery must remain RAM-only under the expected ownership
	// and noexec/nosuid policy, including the legacy-material upgrade path.
	if h.Tmpfs[config.ContainerTempMount] == "" || h.Tmpfs[config.ContainerSecretsMount] != secretsTmpfsSpec {
		return denied
	}
	for destination := range h.Tmpfs {
		if destination != config.ContainerTempMount && destination != config.ContainerSecretsMount {
			return denied
		}
	}
	for _, m := range c.Mounts {
		if seen[m.Destination] {
			return denied
		}
		seen[m.Destination] = true
		switch m.Type {
		case mount.TypeTmpfs:
			if m.Destination != config.ContainerTempMount && m.Destination != config.ContainerSecretsMount && m.Destination != "/dev/shm" {
				return denied
			}
		case mount.TypeBind:
			// The immutable native agent CLI (#5491) is the third trusted
			// read-only bind, accepted only from this provider's staged
			// artifact (#5549); without a staged artifact it stays denied.
			agentCLI := p.cfg.agentCLIArtifact != nil && m.Destination == agentCLIExecutable && m.Source == p.cfg.agentCLIArtifact.Path
			if m.RW || !((m.Destination == managedlaunch.LauncherPath && m.Source == p.cfg.SidecarBinaryPath) || (m.Destination == "/usr/local/bin/entrypoint.sh" && m.Source == p.cfg.EntrypointPath) || agentCLI) {
				return denied
			}
		case mount.TypeVolume:
			if m.Name == "" {
				return denied
			}
			got, err := p.client.VolumeInspect(ctx, m.Name, client.VolumeInspectOptions{})
			if err != nil {
				return denied
			}
			v := got.Volume
			if v.Driver != "local" {
				return denied
			}
			if parts, ok := expected[m.Destination]; ok {
				source, err := safepath.JoinUnder(p.cfg.OutputBasePath, parts...)
				if err != nil {
					return denied
				}
				resolved, err := filepath.EvalSymlinks(source)
				if err != nil {
					return denied
				}
				actual, err := filepath.EvalSymlinks(v.Options["device"])
				if err != nil || actual != resolved {
					return denied
				}
				if v.Labels[crewCrewIDLabel] != crew || v.Labels[resourcelifecycle.InstanceLabel] != p.cfg.InstanceID || len(v.Options) != 3 || v.Options["type"] != "none" || v.Options["o"] != noexecBindMountOpts {
					return denied
				}
			} else {
				want := ""
				switch m.Destination {
				case "/home/agent":
					want = p.homeVolumeName(crew, labels[crewCrewLabel])
				case "/opt/crew-tools":
					want = p.toolsVolumeName(crew, labels[crewCrewLabel])
				default:
					return denied
				}
				if m.Name != want || len(v.Options) != 0 {
					return denied
				}
			}
		default:
			return denied
		}
	}
	required := []string{"/output", "/workspace", "/crew", "/home/agent", "/opt/crew-tools", managedlaunch.LauncherPath, "/usr/local/bin/entrypoint.sh"}
	if p.cfg.agentCLIArtifact != nil {
		required = append(required, agentCLIExecutable)
		// Re-verify the staged bytes here too, like the launcher, so this
		// attestation does not depend on qualifyAgentCLI having just run.
		if runtimestage.VerifyAgentCLIArtifact(p.cfg.agentCLIArtifact) != nil {
			return denied
		}
	}
	for _, destination := range required {
		if !seen[destination] {
			return denied
		}
	}
	if !slices.Contains(h.CapDrop, "ALL") {
		// This stronger profile also requires empty capabilities. Admission
		// checks the running native CLI only when an artifact is configured.
		if p.cfg.agentCLIArtifact == nil {
			return denied
		}
		return p.qualifyAgentCLI(ctx, id)
	}
	return nil
}
