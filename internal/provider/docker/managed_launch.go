package docker

import (
	"context"
	"errors"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
)

func covers(a, b string) bool {
	return a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/") || a == "/"
}

// AttestManagedLaunch is Docker metadata/file-stat inspection, never an
// in-container PATH probe. The orchestrator holds the matching RuntimeUse.
func (p *Provider) AttestManagedLaunch(ctx context.Context, id string, d managedlaunch.Descriptor) error {
	denied := errors.New("managed launch: runtime/image/launcher attestation failed")
	if d.Validate() != nil || p.cfg.SidecarBinaryPath == "" {
		return denied
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	got, err := p.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return denied
	}
	c := got.Container
	h := c.HostConfig
	if c.ID != id || c.Image != d.ImageID || c.State == nil || !c.State.Running || h == nil ||
		!h.ReadonlyRootfs || h.Privileged || len(h.CapAdd) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 ||
		(!slices.Contains(h.SecurityOpt, "no-new-privileges") && !slices.Contains(h.SecurityOpt, "no-new-privileges:true")) {
		return denied
	}
	launcherBound := false
	for _, mount := range c.Mounts {
		if mount.Destination == managedlaunch.LauncherPath && mount.Source == p.cfg.SidecarBinaryPath && !mount.RW && mount.Type == "bind" {
			launcherBound = true
			continue
		}
		if path.Clean(mount.Destination) != mount.Destination || covers(mount.Destination, d.Path) || covers(mount.Destination, managedlaunch.LauncherPath) {
			return denied
		}
	}
	if !launcherBound {
		return denied
	}
	// An image symlink ancestor could map /usr or /opt into a writable home
	// even when no mount's textual destination overlaps. Reject these aliases.
	parents := map[string]bool{}
	for _, executable := range []string{d.Path, managedlaunch.LauncherPath} {
		for dir := path.Dir(executable); dir != "/"; dir = path.Dir(dir) {
			parents[dir] = true
			if len(parents) > 32 {
				return denied
			}
		}
	}
	for dir := range parents {
		st, err := p.client.ContainerStatPath(ctx, id, client.ContainerStatPathOptions{Path: dir})
		if err != nil || !st.Stat.Mode.IsDir() || st.Stat.LinkTarget != "" {
			return denied
		}
	}
	// The FIRST process must already be immune to loader injection. Merely
	// clearing LD_* after starting a dynamic image wrapper would be too late.
	raw, err := os.ReadFile(p.cfg.SidecarBinaryPath)
	if err != nil {
		return denied
	}
	if _, err := managedlaunch.Capture("/opt/crewship/launcher", raw); err != nil {
		return denied
	}
	return nil
}
