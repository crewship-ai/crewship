//go:build linux

package appengine

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// The qualified deployment runs the listener under the application service UID.
// Check path custody before connecting and kernel peer credentials before HTTP
// can send the service secret. Same-UID processes remain inside this trust boundary.
func dialPrivateWorker(ctx context.Context, socket string) (net.Conn, error) {
	dir, err := os.Lstat(filepath.Dir(socket))
	if err != nil || !dir.IsDir() || (dir.Mode().Perm() != 0700 && dir.Mode().Perm() != 0750) {
		return nil, ErrEngineUnavailable
	}
	path, err := os.Lstat(socket)
	if err != nil || path.Mode()&os.ModeSocket == 0 || path.Mode().Perm() != 0600 {
		return nil, ErrEngineUnavailable
	}
	uid := uint32(os.Geteuid())
	ds, dok := dir.Sys().(*syscall.Stat_t)
	ss, sok := path.Sys().(*syscall.Stat_t)
	if !dok || !sok || ds.Uid != uid || ss.Uid != uid {
		return nil, ErrEngineUnavailable
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	fail := func() (net.Conn, error) { _ = conn.Close(); return nil, ErrEngineUnavailable }
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return fail()
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return fail()
	}
	var peer *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || peerErr != nil || peer == nil || peer.Uid != uid {
		return fail()
	}
	return conn, nil
}
