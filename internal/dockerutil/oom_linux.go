package dockerutil

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Reuse a confirmed Docker socket until its inode changes. A Podman
// service's PID and OOM setting can change on every socket activation.
var dockerSocket atomic.Pointer[os.FileInfo]

// With systemd socket activation SO_PEERCRED can identify the user manager
// that opened the listener. SCM_CREDENTIALS identifies the process that
// actually wrote the HTTP response, including on an inherited listener.
func rootlessPodmanOOMScoreAdj(ctx context.Context, cli any, endpoint string) (int, error) {
	path, local := strings.CutPrefix(endpoint, "unix://")
	if !local {
		return 0, nil
	}
	// A socket owned by root is the normal rootful Docker/Podman fast path.
	st, err := os.Stat(path)
	if err != nil {
		return 0, nil
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok && stat.Uid == 0 {
		return 0, nil
	}

	if prior := dockerSocket.Load(); prior != nil && os.SameFile(*prior, st) && (*prior).ModTime() == st.ModTime() {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	isPodman, versionErr := DetectPodman(ctx, cli)
	if versionErr == nil && !isPodman {
		dockerSocket.Store(&st)
		return 0, nil
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	sock := conn.(*net.UnixConn)
	response, control, err := unixHTTPPeer(sock, http.MethodGet, "/info")
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.Header.Get("Libpod-Api-Version") == "" {
		if isPodman {
			return 0, fmt.Errorf("Podman info missing Libpod API version")
		}
		return 0, nil
	}
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Podman info: %s", response.Status)
	}
	var info struct{ SecurityOptions []string }
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		return 0, err
	}
	if !slices.Contains(info.SecurityOptions, "name=rootless") {
		return 0, nil
	}
	cred, err := peerCredentials(control)
	if err != nil {
		return 0, err
	}
	if cred.Uid == 0 {
		return 0, nil
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/oom_score_adj", cred.Pid))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func unixHTTPPeer(sock *net.UnixConn, method, path string) (*http.Response, []byte, error) {
	raw, err := sock.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	var optErr error
	if err = raw.Control(func(fd uintptr) { optErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) }); err != nil {
		return nil, nil, err
	}
	if optErr != nil {
		return nil, nil, optErr
	}
	if _, err = io.WriteString(sock, method+" "+path+" HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
		return nil, nil, err
	}
	var first [1]byte
	oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred))
	n, control, _, _, err := sock.ReadMsgUnix(first[:], oob)
	if err != nil {
		return nil, nil, err
	}
	if n != 1 {
		return nil, nil, io.ErrUnexpectedEOF
	}
	reader := bufio.NewReader(io.LimitReader(io.MultiReader(bytes.NewReader(first[:]), sock), 1<<20))
	response, err := http.ReadResponse(reader, &http.Request{Method: method})
	if err != nil {
		return nil, nil, err
	}
	return response, oob[:control], nil
}

func peerCredentials(control []byte) (*unix.Ucred, error) {
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		return nil, err
	}
	for _, msg := range messages {
		if msg.Header.Level == unix.SOL_SOCKET && msg.Header.Type == unix.SCM_CREDENTIALS {
			cred, err := unix.ParseUnixCredentials(&msg)
			if err != nil {
				return nil, err
			}
			if cred.Pid <= 0 {
				return nil, fmt.Errorf("Podman service PID unavailable")
			}
			return cred, nil
		}
	}
	return nil, fmt.Errorf("Podman service credentials unavailable")
}
