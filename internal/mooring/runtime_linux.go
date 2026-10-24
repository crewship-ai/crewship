//go:build linux

package mooring

import (
	"context"
	"net"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func startRuntime(ctx context.Context, c RuntimeConfig, cmd *exec.Cmd) (*Runtime, error) {
	return startRuntimeSelected(ctx, c, cmd, false)
}
func startRuntimeSelected(_ context.Context, c RuntimeConfig, cmd *exec.Cmd, session bool) (*Runtime, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(fds[0]), "mooring-host")
	child := os.NewFile(uintptr(fds[1]), "mooring-child")
	defer child.Close()
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		return nil, err
	}
	// Never inherit backend encryption/provider credentials or loader options.
	cmd.Env = []string{"LANG=C.UTF-8", "TZ=UTC"}
	cmd.ExtraFiles = []*os.File{child}
	if err = cmd.Start(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	r := &Runtime{RPC: &RPC{conn: conn}, cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		_ = r.RPC.Close()
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(r.done)
	}()
	if session {
		err = runtimeSessionHandshake(r.RPC, c.Versions, c.ControlProfile)
	} else {
		err = runtimeHandshake(r.RPC, c.Versions, c.ControlProfile)
	}
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	r.selectedVersions = c.Versions
	r.selectedControlProfile = c.ControlProfile
	return r, nil
}
