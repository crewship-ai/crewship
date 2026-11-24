package dockerutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Probe on the connection the create request will actually use. A Unix socket
// can forward to another machine, and the listener's SO_PEERCRED may identify
// systemd. SCM_CREDENTIALS on the HTTP reply names the actual writer.
func dialPodmanBinds(ctx context.Context, socket string, binds []string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			conn.Close()
		}
	}()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	resp, control, err := unixHTTPPeer(conn.(*net.UnixConn), http.MethodHead, "/_ping")
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Close || resp.Header.Get("Libpod-Api-Version") == "" {
		return nil, errors.New("SELinux relabel requires a persistent local Podman connection")
	}
	cred, err := peerCredentials(control)
	if err != nil {
		return nil, err
	}
	// Hold the process directory across all checks, including PID reuse.
	proc, err := os.Open(fmt.Sprintf("/proc/%d", cred.Pid))
	if err != nil {
		return nil, err
	}
	defer proc.Close()
	dir := fmt.Sprintf("/proc/self/fd/%d", proc.Fd())
	installed, err := os.Stat("/usr/bin/podman")
	if err != nil {
		return nil, fmt.Errorf("identify installed Podman for SELinux relabel: %w", err)
	}
	exe, err := os.Stat(filepath.Join(dir, "exe"))
	if err != nil || !os.SameFile(exe, installed) {
		return nil, errors.New("SELinux relabel peer is not the installed Podman executable")
	}
	args, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return nil, err
	}
	// A podman system dial-stdio process can itself be a forwarder.
	if !localPodmanServiceCommand(args) {
		return nil, errors.New("SELinux relabel peer is not a local Podman system service")
	}
	if err := verifyRelabelSources(filepath.Join(dir, "root"), binds); err != nil {
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	success = true
	return conn, nil
}

func localPodmanServiceCommand(cmdline []byte) bool {
	args := strings.Split(string(bytes.TrimSuffix(cmdline, []byte{0})), "\x00")
	// Recognize the packaged systemd unit and the default manual service.
	// Unknown forms must not authorize a host relabel.
	args = args[1:]
	if len(args) > 0 && strings.HasPrefix(args[0], "--log-level=") {
		args = args[1:]
	}
	return len(args) >= 2 && args[0] == "system" && args[1] == "service"
}

func verifyRelabelSources(serviceRoot string, binds []string) error {
	root, err := os.OpenRoot(serviceRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, bind := range binds {
		if !sharedCrewRelabel(bind) {
			continue
		}
		src, _, _ := strings.Cut(bind, ":")
		if !filepath.IsAbs(src) || filepath.Clean(src) != src || src == "/" {
			return fmt.Errorf("invalid SELinux relabel source %q", src)
		}
		if canonical, err := filepath.EvalSymlinks(src); err != nil || canonical != src {
			return fmt.Errorf("SELinux bind source %q no longer resolves to its approved path", src)
		}
		local, err := os.Stat(src)
		if err != nil {
			return err
		}
		// The engine opens this pathname again at start. Each parent must
		// protect its child from replacement by other host users, including
		// through a different parent in the service's mount namespace.
		for child := src; ; {
			relative := strings.TrimPrefix(child, "/")
			if relative == "" {
				relative = "."
			}
			remote, err := root.Lstat(relative)
			if err != nil {
				stat := local.Sys().(*syscall.Stat_t)
				return fmt.Errorf("inspect Podman SELinux bind path %q (local dev=%d ino=%d): %w", child, stat.Dev, stat.Ino, err)
			}
			if !os.SameFile(local, remote) {
				here, peer := local.Sys().(*syscall.Stat_t), remote.Sys().(*syscall.Stat_t)
				return fmt.Errorf("Podman sees a different SELinux bind path at %q (local dev=%d ino=%d, peer dev=%d ino=%d)", child, here.Dev, here.Ino, peer.Dev, peer.Ino)
			}
			if child == "/" {
				break
			}
			parent := filepath.Dir(child)
			info, err := os.Lstat(parent)
			if err != nil {
				return err
			}
			if !info.IsDir() || !trustedRelabelOwner(info) ||
				(info.Mode().Perm()&0022 != 0 && (info.Mode()&os.ModeSticky == 0 || !trustedRelabelOwner(local))) {
				return fmt.Errorf("SELinux bind path %q can be replaced in %q", child, parent)
			}
			child, local = parent, info
		}
	}
	return nil
}

func trustedRelabelOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (stat.Uid == 0 || int(stat.Uid) == os.Geteuid())
}
