package docker

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Inspect the running container's mount, including on reuse after a Crewship
// restart. Archive path-stat on running Podman containers enters their mount
// namespace on a daemon thread. Pre-start path-stat can inspect the bind source
// instead of the executing container's actual mounted path.
func verifyPodmanMountedFile(ctx context.Context, cli *client.Client, c container.InspectResponse, parents map[string]bool, target string, host os.FileInfo) error {
	rawID, err := hex.DecodeString(c.ID)
	if err != nil || len(rawID) != 32 || c.HostConfig == nil || c.HostConfig.Init == nil || !*c.HostConfig.Init ||
		c.State == nil || !c.State.Running || c.State.Paused || c.State.Restarting || c.State.Pid <= 1 || c.State.StartedAt == "" {
		return errors.New("cannot identify running Podman container init")
	}
	pid := c.State.Pid
	proc, err := os.Open("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return err
	}
	defer proc.Close()
	dir := fmt.Sprintf("/proc/self/fd/%d", proc.Fd())
	status, err := os.ReadFile(dir + "/status")
	if err != nil {
		return err
	}
	pids, err := statusNamespacePIDs(status, pid)
	if err != nil || len(pids) < 2 || pids[len(pids)-1] != 1 {
		return errors.New("Podman PID is not the container namespace init")
	}
	group, err := os.ReadFile(dir + "/cgroup")
	if err != nil || !podmanContainerCgroup(string(group), c.ID) {
		return errors.New("Podman init cgroup differs from the inspected container")
	}
	root, err := os.OpenRoot(dir + "/root")
	if err != nil {
		return err
	}
	defer root.Close()
	if err := verifyMountedFile(root, parents, target, host); err != nil {
		return err
	}
	current, err := cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	now := current.Container.State
	if current.Container.ID != c.ID || now == nil || !now.Running || now.Paused || now.Restarting || now.Pid != pid || now.StartedAt != c.State.StartedAt {
		return errors.New("Podman container changed during mount inspection")
	}
	return nil
}

func podmanContainerCgroup(groups, id string) bool {
	scope := "/libpod-" + id + ".scope"
	for _, line := range strings.Split(groups, "\n") {
		if dir, ok := strings.CutPrefix(line, "0::"); ok {
			return strings.HasSuffix(dir, scope) || strings.HasSuffix(dir, scope+"/container")
		}
	}
	return false
}

func verifyMountedFile(root *os.Root, parents map[string]bool, target string, host os.FileInfo) error {
	checkDir := func(dir string) error {
		fi, err := root.Lstat(strings.TrimPrefix(dir, "/"))
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("mounted executable parent %q is not a directory", dir)
		}
		return nil
	}
	for dir := path.Dir(target); dir != "/"; dir = path.Dir(dir) {
		if err := checkDir(dir); err != nil {
			return err
		}
	}
	for dir := range parents {
		if err := checkDir(dir); err != nil {
			return err
		}
	}
	fi, err := root.Lstat(strings.TrimPrefix(target, "/"))
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || !os.SameFile(fi, host) {
		return fmt.Errorf("mounted executable %q differs from its verified host inode", target)
	}
	return nil
}
