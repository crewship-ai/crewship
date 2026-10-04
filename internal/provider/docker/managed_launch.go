package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

func covers(a, b string) bool {
	return a == b || strings.HasPrefix(b, a+"/") || strings.HasPrefix(a, b+"/") || a == "/"
}

// AttestManagedLaunch is Docker metadata/file-stat inspection, never an
// in-container PATH probe. The orchestrator holds the matching RuntimeUse.
func (p *Provider) AttestManagedLaunch(ctx context.Context, id string, d managedlaunch.Descriptor) error {
	if err := managedLauncherServerUIDError(os.Geteuid()); err != nil {
		return err
	}
	return p.attestManagedLaunch(ctx, id, d, safeLauncherOwner, safeLauncherDirectory)
}

// attestManagedLaunch separates runtime validation from the host trust policy.
// Production always enters through AttestManagedLaunch and its UID refusal;
// unit fixtures can validate Docker controls under otherwise unsupported UIDs.
func (p *Provider) attestManagedLaunch(ctx context.Context, id string, d managedlaunch.Descriptor, owner, directory func(os.FileInfo) bool) error {
	denied := errors.New("managed launch: runtime/image/launcher attestation failed")
	if d.Validate() != nil || p.cfg.SidecarBinaryPath == "" {
		return denied
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
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
	// Docker can omit HostConfig.Tmpfs destinations from Mounts. They can
	// obscure trusted code even when the image root is read-only.
	for destination := range h.Tmpfs {
		if path.Clean(destination) != destination || !path.IsAbs(destination) || covers(destination, d.Path) || covers(destination, managedlaunch.LauncherPath) {
			return denied
		}
	}
	launcherBound := false
	for _, mount := range c.Mounts {
		if mount.Destination == managedlaunch.LauncherPath {
			if mount.Source != p.cfg.SidecarBinaryPath {
				return errors.New("managed launch: recreate runtime with current immutable launcher generation required")
			}
			if mount.RW || mount.Type != "bind" {
				return denied
			}
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
	f, err := os.Open(p.cfg.SidecarBinaryPath)
	if err != nil {
		return denied
	}
	raw, err := io.ReadAll(io.LimitReader(f, managedlaunch.MaxArtifactBytes+1))
	f.Close()
	if err != nil {
		return denied
	}
	hostArtifact, err := managedlaunch.Capture("/opt/crewship/launcher", raw)
	if err != nil {
		return denied
	}
	// Require immutable, digest-addressed staging. Fixed-name binds may hold
	// an older inode, even when the archive API returns the current host file.
	if filepath.Base(p.cfg.SidecarBinaryPath) != runtimestage.SidecarFileName+"-"+hostArtifact.SHA256 {
		return errors.New("managed launch: recreate runtime with immutable launcher staging required")
	}
	info, err := os.Lstat(p.cfg.SidecarBinaryPath)
	parent, parentErr := os.Lstat(filepath.Dir(p.cfg.SidecarBinaryPath))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0222 != 0 || !owner(info) || parentErr != nil || !directory(parent) {
		return denied
	}
	// Secondary consistency check; archive reads do not attest live bind inodes.
	copied, err := p.client.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: managedlaunch.LauncherPath})
	if err != nil {
		return denied
	}
	defer copied.Content.Close()
	limited := &io.LimitedReader{R: copied.Content, N: managedlaunch.MaxArtifactBytes + 8192}
	tr := tar.NewReader(limited)
	entry, err := tr.Next()
	if err != nil || entry.Typeflag != tar.TypeReg || entry.Name != path.Base(managedlaunch.LauncherPath) || entry.Size != int64(len(raw)) {
		return denied
	}
	live, err := io.ReadAll(tr)
	if err != nil || !bytes.Equal(live, raw) {
		return denied
	}
	expected := p.ExpectedSidecarHash()
	if _, err := tr.Next(); err != io.EOF || limited.N <= 0 || expected == "" || !strings.HasPrefix(hostArtifact.SHA256, expected) {
		return denied
	}
	return nil
}
