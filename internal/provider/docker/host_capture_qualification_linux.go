//go:build linux

package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

const hostStopProbeProtocol = "crewship-run-stop-process-v1"

// QualifyHostCapture runs before a protected claim or workload exec. Old or
// unqualified readonly helper binds use the legacy transport; daemon/inspection
// errors refuse admission rather than silently downgrade a qualified runtime.
func (p *Provider) QualifyHostCapture(ctx context.Context, containerID string) (bool, error) {
	if !p.SupportsHostCapture() || p.cfg.SidecarBinaryPath == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	inspected, err := p.client.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	c := inspected.Container
	if c.State == nil || !c.State.Running || c.ID == "" || c.Image == "" || c.State.StartedAt == "" {
		return false, errors.New("host capture qualification requires a running immutable container")
	}
	trusted, err := filepath.EvalSymlinks(p.cfg.SidecarBinaryPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !trustedHostHelperPath(trusted) {
		return false, nil
	}
	bound := false
	for _, m := range c.Mounts {
		if m.Destination != "/usr/local/bin/crewship-sidecar" {
			continue
		}
		if m.Type != mount.TypeBind || m.RW {
			return false, nil
		}
		source, err := filepath.EvalSymlinks(m.Source)
		if err != nil {
			return false, err
		}
		if source != trusted {
			return false, nil
		}
		bound = true
	}
	if !bound {
		return false, nil
	}
	sourceBefore, err := os.Stat(trusted)
	if err != nil {
		return false, err
	}
	f, err := os.Open(trusted)
	if err != nil {
		return false, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if err = errors.Join(copyErr, closeErr); err != nil {
		return false, err
	}
	expected := hex.EncodeToString(hash.Sum(nil))
	probe, err := p.Exec(ctx, provider.ExecConfig{ContainerID: c.ID, User: "1001:1001", Cmd: []string{"/usr/local/bin/crewship-sidecar", "--host-capture-probe-v1"}})
	if err != nil {
		return false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(probe.Reader, 257))
	closeErr = probe.Reader.Close()
	if err = errors.Join(readErr, closeErr); err != nil {
		return false, err
	}
	code, err := provider.WaitExecExit(ctx, p, probe.ExecID, 5*time.Second)
	if err != nil {
		return false, err
	}
	fresh, err := p.client.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
	if err != nil {
		return false, err
	}
	if fresh.Container.State == nil || !fresh.Container.State.Running || fresh.Container.ID != c.ID || fresh.Container.Image != c.Image || fresh.Container.State.StartedAt != c.State.StartedAt {
		return false, errors.New("container identity changed during host capture qualification")
	}
	if code != 0 || len(data) > 256 {
		return false, nil
	}
	sourceAfter, err := os.Stat(trusted)
	if err != nil {
		return false, err
	}
	if !os.SameFile(sourceBefore, sourceAfter) || !sourceBefore.ModTime().Equal(sourceAfter.ModTime()) || sourceBefore.Size() != sourceAfter.Size() {
		return false, errors.New("host helper source changed during qualification")
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 || fields[0] != hostStopProbeProtocol || fields[1] != expected {
		return false, nil
	}
	return true, nil
}

// The mounted binary and each canonical ancestor must be controlled by the
// controller/root. Root-owned sticky /tmp permits private fixture/install dirs.
func trustedHostHelperPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if info.Mode().Perm()&0022 != 0 && !(stat.Uid == 0 && info.IsDir() && info.Mode()&os.ModeSticky != 0) {
			return false
		}
		if current == path {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return false
			}
		} else if !info.IsDir() {
			return false
		}
		if current == filepath.Dir(current) {
			return true
		}
	}
}
