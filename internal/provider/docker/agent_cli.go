package docker

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moby/moby/api/types/mount"

	"github.com/crewship-ai/crewship/internal/buildinfo"
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
