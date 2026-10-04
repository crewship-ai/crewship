//go:build integration || conformance

package docker

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// reclaimBindOwnership hands the harness's temp tree back to the user running
// the test, through a root container, so the tree can be deleted afterwards.
//
// Why it has to exist at all: the product deliberately chowns the crew's bind
// dirs to 1001:1001 (fixBindMountOwnership), which is the behaviour these
// harnesses exist to exercise. Removing a directory needs write permission on
// its PARENT, and chmod needs ownership — neither of which a host user that is
// not uid 1001 has once the product has run. So the leftover tree is not merely
// awkward to delete, it is undeletable from the host, and every conformance run
// on a host whose uid is not 1001 failed in cleanup with every assertion green
// (#2005). The only tool that can undo a root container's chown is another root
// container, which is the same trick fixBindMountOwnership itself uses.
//
// # Which uid to chown to
//
// Not $(id -u) read inside the container, and not a hardcoded number. Under a
// ROOTLESS runtime the container's uid 0 IS the invoking host user, so the
// right target is 0:0; under a rootful one it is the host user's real uid.
// Rather than detect rootless-ness — nothing in this package models it, and the
// answer differs again on VM-backed runtimes — the target is read off the bind
// itself: `stat` the mount point, which is this temp root, created by this
// process and never handed to the product. Whatever uid the container sees for
// it is by construction the container-side name for "the user running this
// test", on either kind of runtime. Chowning the tree to that is correct
// without anything having to know which kind it is.
//
// Best-effort by contract: a reclaim that can fail the suite would reintroduce
// exactly the class of failure #2005 is about — a green run reported as red by
// its own cleanup. Every error is logged and the tree is leaked instead.
func reclaimBindOwnership(t *testing.T, host, image, base string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cli, err := client.New(client.WithHost(host))
	if err != nil {
		t.Logf("ownership reclaim: client for %s: %v (leaking %s)", host, err, base)
		return
	}
	defer cli.Close()

	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: image,
			User:  "0:0",
			// `-h` so a symlink in the tree cannot redirect the chown outside
			// the bind, and `--` so a path is never read as an option.
			Entrypoint: []string{"sh", "-c", `own=$(stat -c '%u:%g' /mnt) && exec chown -Rh "$own" -- /mnt`},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "none",
			Mounts:      []mount.Mount{{Type: mount.TypeBind, Source: base, Target: "/mnt"}},
		},
	})
	if err != nil {
		t.Logf("ownership reclaim: create: %v (leaking %s)", err, base)
		return
	}
	defer func() {
		rmCtx, rmCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer rmCancel()
		_, _ = cli.ContainerRemove(rmCtx, created.ID, client.ContainerRemoveOptions{Force: true})
	}()

	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Logf("ownership reclaim: start: %v (leaking %s)", err, base)
		return
	}
	wait := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case res := <-wait.Result:
		if res.StatusCode != 0 {
			t.Logf("ownership reclaim: chown container exited %d (leaking %s)", res.StatusCode, base)
		}
	case werr := <-wait.Error:
		t.Logf("ownership reclaim: wait: %v (leaking %s)", werr, base)
	case <-ctx.Done():
		t.Logf("ownership reclaim: timed out: %v (leaking %s)", ctx.Err(), base)
	}
}
