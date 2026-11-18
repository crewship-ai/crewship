package docker

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/moby/moby/api/types/mount"
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
